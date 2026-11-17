//! Local key mode: s2n-tls hands every CertificateVerify signature to an
//! asynchronous private key callback. The callback signs at once and returns
//! a future that releases the signature after the configured delay, so the
//! endpoint keeps driving other connections while one signature is pending.

use std::future::Future;
use std::pin::Pin;
use std::task::{Context, Poll};
use std::time::Duration;

use aws_lc_rs::digest::{self, Digest};
use aws_lc_rs::signature::{self, EcdsaKeyPair, RsaEncoding, RsaKeyPair};
use rustls::pki_types::pem::PemObject;
use rustls::pki_types::PrivateKeyDer;
use s2n_quic::provider::tls::s2n_tls::callbacks::{
    ConnectionFuture, OperationType, PrivateKeyCallback, PrivateKeyOperation,
};
use s2n_quic::provider::tls::s2n_tls::connection::Connection;
use s2n_quic::provider::tls::s2n_tls::enums::{HashAlgorithm, SignatureAlgorithm};
use s2n_quic::provider::tls::s2n_tls::error::Error;

enum Key {
    Ecdsa(EcdsaKeyPair),
    Rsa(RsaKeyPair),
}

pub struct DelayedKey {
    key: Key,
    delay: Duration,
}

impl DelayedKey {
    pub fn load(path: &str, delay: Duration) -> Result<Self, String> {
        let der = PrivateKeyDer::from_pem_file(path).map_err(|e| format!("read {path}: {e}"))?;
        let key = match &der {
            PrivateKeyDer::Pkcs8(pkcs8) => {
                let bytes = pkcs8.secret_pkcs8_der();
                match EcdsaKeyPair::from_pkcs8(&signature::ECDSA_P256_SHA256_ASN1_SIGNING, bytes) {
                    Ok(key) => Key::Ecdsa(key),
                    Err(_) => Key::Rsa(RsaKeyPair::from_pkcs8(bytes).map_err(|e| {
                        format!("{path}: neither a P-256 nor an RSA key: {e}")
                    })?),
                }
            },
            PrivateKeyDer::Sec1(sec1) => Key::Ecdsa(
                EcdsaKeyPair::from_private_key_der(
                    &signature::ECDSA_P256_SHA256_ASN1_SIGNING,
                    sec1.secret_sec1_der(),
                )
                .map_err(|e| format!("{path}: not a P-256 key: {e}"))?,
            ),
            PrivateKeyDer::Pkcs1(pkcs1) => Key::Rsa(
                RsaKeyPair::from_der(pkcs1.secret_pkcs1_der())
                    .map_err(|e| format!("{path}: not an RSA key: {e}"))?,
            ),
            _ => return Err(format!("{path}: unsupported key format")),
        };
        Ok(Self { key, delay })
    }

    /// Signs the digest that s2n-tls computed over the CertificateVerify input.
    fn sign(&self, kind: &OperationType, input: &[u8]) -> Result<Vec<u8>, String> {
        let unsupported = || format!("unsupported operation {kind:?} for this key");
        let OperationType::Sign(algorithm, hash) = kind else {
            return Err(unsupported());
        };
        let (digest_alg, pss): (&'static digest::Algorithm, &'static dyn RsaEncoding) =
            match hash {
                HashAlgorithm::SHA256 => (&digest::SHA256, &signature::RSA_PSS_SHA256),
                HashAlgorithm::SHA384 => (&digest::SHA384, &signature::RSA_PSS_SHA384),
                HashAlgorithm::SHA512 => (&digest::SHA512, &signature::RSA_PSS_SHA512),
                _ => return Err(unsupported()),
            };
        let digest = Digest::import_less_safe(input, digest_alg).map_err(|_| unsupported())?;
        match (&self.key, algorithm) {
            (Key::Ecdsa(key), SignatureAlgorithm::ECDSA) => key
                .sign_digest(&digest)
                .map(|sig| sig.as_ref().to_vec())
                .map_err(|_| unsupported()),
            (Key::Rsa(key), SignatureAlgorithm::RSA_PSS_RSAE) => {
                let mut sig = vec![0; key.public_modulus_len()];
                key.sign_digest(pss, &digest, &mut sig).map_err(|_| unsupported())?;
                Ok(sig)
            },
            _ => Err(unsupported()),
        }
    }
}

impl PrivateKeyCallback for DelayedKey {
    fn handle_operation(
        &self, _connection: &mut Connection, operation: PrivateKeyOperation,
    ) -> Result<Option<Pin<Box<dyn ConnectionFuture>>>, Error> {
        let mut input = vec![0; operation.input_size()?];
        operation.input(&mut input)?;
        let signature =
            self.sign(operation.kind()?, &input).map_err(|e| Error::application(e.into()))?;
        Ok(Some(Box::pin(PendingSignature {
            operation: Some(operation),
            signature,
            ready: Box::pin(tokio::time::sleep(self.delay)),
        })))
    }
}

struct PendingSignature {
    operation: Option<PrivateKeyOperation>,
    signature: Vec<u8>,
    ready: Pin<Box<tokio::time::Sleep>>,
}

impl ConnectionFuture for PendingSignature {
    fn poll(
        mut self: Pin<&mut Self>, connection: &mut Connection, cx: &mut Context,
    ) -> Poll<Result<(), Error>> {
        if self.ready.as_mut().poll(cx).is_pending() {
            return Poll::Pending;
        }
        let operation = self.operation.take().expect("polled after completion");
        Poll::Ready(operation.set_output(connection, &self.signature))
    }
}
