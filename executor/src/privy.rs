use base64::{engine::general_purpose::STANDARD, Engine};
use serde::{Deserialize, Serialize};

use crate::{auth::Authorizer, venues::Call};

const PRIVY_API: &str = "https://api.privy.io";

#[derive(Debug, thiserror::Error)]
pub enum PrivyError {
    #[error("privy request failed: {0}")]
    Http(#[from] reqwest::Error),
    #[error("privy returned {status}: {body}")]
    Status { status: u16, body: String },
}

pub struct Privy {
    http: reqwest::Client,
    app_id: String,
    basic_auth: String,
    authorizer: Option<Authorizer>,
}

#[derive(Debug, Serialize)]
struct TxRequest<'a> {
    method: &'static str,
    caip2: String,
    chain_type: &'static str,
    params: TxParams<'a>,
    #[serde(skip_serializing_if = "Option::is_none")]
    reference_id: Option<&'a str>,
}

#[derive(Debug, Serialize)]
struct TxParams<'a> {
    transaction: Tx<'a>,
}

#[derive(Debug, Serialize)]
struct Tx<'a> {
    to: String,
    data: String,
    value: &'a str,
    chain_id: u64,
}

#[derive(Debug, Deserialize)]
pub struct TxResponse {
    pub data: TxData,
}

#[derive(Debug, Deserialize)]
pub struct TxData {
    pub hash: String,
    pub transaction_id: String,
}

impl Privy {
    pub fn new(app_id: String, app_secret: String, authorizer: Option<Authorizer>) -> Self {
        let basic_auth = STANDARD.encode(format!("{app_id}:{app_secret}"));
        Self {
            http: reqwest::Client::new(),
            app_id,
            basic_auth,
            authorizer,
        }
    }

    async fn post_signed(
        &self,
        url: &str,
        body: &serde_json::Value,
    ) -> Result<reqwest::Response, reqwest::Error> {
        let mut req = self
            .http
            .post(url)
            .header("Authorization", format!("Basic {}", self.basic_auth))
            .header("privy-app-id", &self.app_id)
            .header("Content-Type", "application/json");
        if let Some(sig) = self.signature_header(url, body) {
            req = req.header("privy-authorization-signature", sig);
        }
        req.json(body).send().await
    }

    fn signature_header(&self, url: &str, body: &serde_json::Value) -> Option<String> {
        Some(self.authorizer.as_ref()?.sign(url, &self.app_id, body))
    }

    pub async fn send_transaction(
        &self,
        wallet_id: &str,
        chain_id: u64,
        call: &Call,
        reference_id: &str,
    ) -> Result<TxData, PrivyError> {
        let body = TxRequest {
            method: "eth_sendTransaction",
            caip2: format!("eip155:{chain_id}"),
            chain_type: "ethereum",
            params: TxParams {
                transaction: Tx {
                    to: format!("{:#x}", call.to),
                    data: format!("0x{}", alloy_primitives::hex::encode(&call.data)),
                    value: "0x0",
                    chain_id,
                },
            },
            reference_id: Some(reference_id),
        };

        let body = serde_json::to_value(&body).expect("TxRequest is always serializable");
        let url = format!("{PRIVY_API}/v1/wallets/{wallet_id}/rpc");
        let resp = self.post_signed(&url, &body).await?;

        let status = resp.status();
        if !status.is_success() {
            let body = resp.text().await.unwrap_or_default();
            return Err(PrivyError::Status {
                status: status.as_u16(),
                body,
            });
        }
        Ok(resp.json::<TxResponse>().await?.data)
    }

    pub async fn ping(&self) -> bool {
        self.http
            .get(format!("{PRIVY_API}/v1/apps/{}", self.app_id))
            .header("Authorization", format!("Basic {}", self.basic_auth))
            .header("privy-app-id", &self.app_id)
            .send()
            .await
            .map(|r| r.status().is_success())
            .unwrap_or(false)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const TEST_KEY: &str = crate::auth::TEST_KEY;

    fn body() -> serde_json::Value {
        serde_json::json!({"method": "eth_sendTransaction"})
    }

    #[test]
    fn header_present_only_when_a_key_is_configured() {
        let url = "https://api.privy.io/v1/wallets/w1/rpc";
        let signed = Privy::new("app".into(), "s".into(), Some(Authorizer::new(TEST_KEY).unwrap()));
        assert!(signed.signature_header(url, &body()).is_some());

        let unsigned = Privy::new("app".into(), "s".into(), None);
        assert!(unsigned.signature_header(url, &body()).is_none());
    }
}
