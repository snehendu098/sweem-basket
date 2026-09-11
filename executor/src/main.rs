//! Executor: the only component that signs and submits transactions.
//!
//! It accepts requests from the wallet service alone. Funds never enter a wallet
//! this service controls — every transaction is sent from the user's own Privy
//! embedded wallet via a delegated action, bounded by two independent limits:
//! Privy's policy engine, and the local venue allowlist in `venues.json`.

mod auth;
mod config;
mod privy;
mod route;
mod rpc;
mod venues;

use axum::{routing::get, routing::post, Json, Router};
use serde_json::json;
use std::{sync::Arc, time::Duration};
use tracing::info;

use crate::{auth::Authorizer, config::Config, privy::Privy, rpc::Rpc, venues::Registry};

#[derive(Clone)]
pub struct AppState {
    pub privy: Arc<Privy>,
    pub venues: Arc<Registry>,
    /// Read-only node used to confirm each call before the next is submitted.
    pub rpc: Arc<Rpc>,
    pub receipt_timeout: Duration,
}

#[tokio::main]
async fn main() {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| "executor=info,tower_http=info".into()),
        )
        .init();

    if let Err(e) = run().await {
        eprintln!("fatal: {e}");
        std::process::exit(1);
    }
}

async fn run() -> Result<(), String> {
    let cfg = Config::from_env()?;
    let venues = Registry::load(&cfg.venues_path)?;
    info!(count = venues.len(), path = %cfg.venues_path, "venue allowlist loaded");
    info!(rpc = %cfg.rpc_url, receipt_timeout = ?cfg.receipt_timeout, "receipt polling configured");

    // A malformed key is a misconfiguration, so fail fast; an absent one is
    // legitimate for wallets with no authorization-key owner.
    let authorizer = match cfg.privy_authorization_key.as_deref() {
        Some(key) => Some(Authorizer::new(key).map_err(|e| format!("PRIVY_AUTHORIZATION_PRIVATE_KEY: {e}"))?),
        None => {
            tracing::warn!(
                "PRIVY_AUTHORIZATION_PRIVATE_KEY not set - requests are sent unsigned; \
                 wallets owned by an authorization key or key quorum will reject them"
            );
            None
        }
    };
    if authorizer.is_some() {
        info!("privy authorization signatures enabled");
    }

    let state = AppState {
        privy: Arc::new(Privy::new(
            cfg.privy_app_id.clone(),
            cfg.privy_app_secret,
            authorizer,
        )),
        venues: Arc::new(venues),
        rpc: Arc::new(Rpc::new(cfg.rpc_url.clone())),
        receipt_timeout: cfg.receipt_timeout,
    };

    let app = Router::new()
        .route("/health", get(health))
        .route("/route", post(route::route))
        .layer(tower_http::trace::TraceLayer::new_for_http())
        .with_state(state);

    let listener = tokio::net::TcpListener::bind(&cfg.addr)
        .await
        .map_err(|e| format!("bind {}: {e}", cfg.addr))?;
    info!(addr = %cfg.addr, "executor listening");

    axum::serve(listener, app)
        .with_graceful_shutdown(shutdown())
        .await
        .map_err(|e| e.to_string())
}

async fn health(
    axum::extract::State(state): axum::extract::State<AppState>,
) -> Json<serde_json::Value> {
    let privy_up = state.privy.ping().await;
    Json(json!({
        "status": if privy_up { "ok" } else { "degraded" },
        "privy": if privy_up { "up" } else { "down" },
        "venues": state.venues.len(),
    }))
}

async fn shutdown() {
    let _ = tokio::signal::ctrl_c().await;
    info!("shutting down");
}
