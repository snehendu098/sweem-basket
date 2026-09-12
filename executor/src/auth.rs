use base64::{engine::general_purpose::STANDARD, Engine};
use p256::{
    ecdsa::{signature::Signer, DerSignature, SigningKey},
    pkcs8::DecodePrivateKey,
};
use serde_json::{json, Value};

const PREFIX: &str = "wallet-auth:";

#[cfg(test)]
pub const TEST_KEY: &str = "wallet-auth:MIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQg5kN2TyEXdzWAq5FQ0/xjUNzQWwR+LhLrsCbG0ywyaGuhRANCAARYefQCTLnZl3qq03dgaeiVDKh/V5ta9RHTiSq9SyzXkgVWJuFa3PRc8rLwzIeqSjMaidupsBsHWpeR3xdgBJGY";

#[derive(Debug, thiserror::Error)]
pub enum AuthError {
    #[error("authorization key must start with `wallet-auth:`")]
    MissingPrefix,
    #[error("authorization key is not valid base64")]
    BadBase64,
    #[error("authorization key is not a valid PKCS#8 P-256 private key")]
    BadKey,
}

#[derive(Clone)]
pub struct Authorizer {
    key: SigningKey,
}

impl std::fmt::Debug for Authorizer {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("Authorizer(<redacted>)")
    }
}

impl Authorizer {
    pub fn new(authorization_key: &str) -> Result<Self, AuthError> {
        let b64 = authorization_key
            .trim()
            .strip_prefix(PREFIX)
            .ok_or(AuthError::MissingPrefix)?;
        let der = STANDARD.decode(b64).map_err(|_| AuthError::BadBase64)?;
        let key = SigningKey::from_pkcs8_der(&der).map_err(|_| AuthError::BadKey)?;
        Ok(Self { key })
    }

    pub fn sign(&self, url: &str, app_id: &str, body: &Value) -> String {
        let sig: DerSignature = self.key.sign(canonical_payload(url, app_id, body).as_bytes());
        STANDARD.encode(sig.as_bytes())
    }

    #[cfg(test)]
    pub fn verifying_key(&self) -> p256::ecdsa::VerifyingKey {
        *self.key.verifying_key()
    }
}

fn canonical_payload(url: &str, app_id: &str, body: &Value) -> String {
    let body = match body.as_object() {
        Some(o) if o.is_empty() => Value::String(String::new()),
        _ => body.clone(),
    };
    json!({
        "version": 1,
        "method": "POST",
        "url": url.trim_end_matches('/'),
        "body": body,
        "headers": { "privy-app-id": app_id },
    })
    .to_string()
}

#[cfg(test)]
mod tests {
    use super::*;
    use p256::ecdsa::signature::Verifier;

    #[test]
    fn canonical_payload_is_sorted_and_minimal() {
        let got = canonical_payload(
            "https://api.privy.io/v1/wallets/w1/rpc",
            "app-1",
            &json!({"method": "eth_sendTransaction", "caip2": "eip155:8453"}),
        );
        assert_eq!(
            got,
            r#"{"body":{"caip2":"eip155:8453","method":"eth_sendTransaction"},"headers":{"privy-app-id":"app-1"},"method":"POST","url":"https://api.privy.io/v1/wallets/w1/rpc","version":1}"#
        );
    }

    #[test]
    fn empty_body_becomes_empty_string() {
        let got = canonical_payload("https://api.privy.io/v1/wallets", "app-1", &json!({}));
        assert!(got.starts_with(r#"{"body":"","headers""#), "{got}");
    }

    #[test]
    fn trailing_slash_stripped() {
        let got = canonical_payload("https://api.privy.io/v1/wallets/", "a", &json!({}));
        assert!(got.contains(r#""url":"https://api.privy.io/v1/wallets""#), "{got}");
    }

    #[test]
    fn signature_verifies_and_is_deterministic() {
        let a = Authorizer::new(TEST_KEY).unwrap();
        let body = json!({"method": "personal_sign"});
        let url = "https://api.privy.io/v1/wallets/w1/rpc";

        let sig = a.sign(url, "app-1", &body);
        assert_eq!(sig, a.sign(url, "app-1", &body), "RFC 6979 is deterministic");

        let der = STANDARD.decode(&sig).unwrap();
        let parsed = DerSignature::try_from(der.as_slice()).unwrap();
        a.verifying_key()
            .verify(canonical_payload(url, "app-1", &body).as_bytes(), &parsed)
            .expect("signature verifies against derived public key");
    }

    #[test]
    fn matches_privy_sdk_golden_vectors() {
        let a = Authorizer::new(TEST_KEY).unwrap();
        assert_eq!(
            a.sign(
                "https://api.privy.io/v1/wallets/w1/rpc",
                "app-1",
                &json!({"method":"eth_sendTransaction","caip2":"eip155:8453","params":{"transaction":{"to":"0xabc","value":"0x0","chain_id":8453,"data":"0x00"}}}),
            ),
            "MEQCIGtB2l7qLSg9eRu33UWBytH+LOF5ThvYNH03Qi+hF/sXAiA3WNTFeh4X30NPPMFdfIDRaQ4DK5OslLZj3myjHnGFAg=="
        );
        assert_eq!(
            a.sign("https://api.privy.io/v1/wallets", "app-1", &json!({})),
            "MEUCIQDoBeM6a8GZfQephvYgxuA+G4uc+QB/MEpbdTo39/rxUAIgRk8wg//j9q64RoO8OtnbThE72+M1pmYF9F5AAS+wXKI="
        );
    }

    #[test]
    fn signature_covers_the_body() {
        let a = Authorizer::new(TEST_KEY).unwrap();
        let url = "https://api.privy.io/v1/wallets/w1/rpc";
        assert_ne!(
            a.sign(url, "app-1", &json!({"to": "0x1"})),
            a.sign(url, "app-1", &json!({"to": "0x2"}))
        );
    }

    #[test]
    fn key_parsing_rejects_bad_input() {
        assert!(matches!(
            Authorizer::new(TEST_KEY.trim_start_matches("wallet-auth:")),
            Err(AuthError::MissingPrefix)
        ));
        assert!(matches!(
            Authorizer::new("wallet-auth:not!base64!"),
            Err(AuthError::BadBase64)
        ));
        assert!(matches!(
            Authorizer::new("wallet-auth:aGVsbG8="),
            Err(AuthError::BadKey)
        ));
    }

    #[test]
    fn debug_does_not_leak_key() {
        let out = format!("{:?}", Authorizer::new(TEST_KEY).unwrap());
        assert_eq!(out, "Authorizer(<redacted>)");
    }
}
