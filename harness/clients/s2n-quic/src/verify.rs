//! Server certificate verification with the semantics of the Go client's
//! RootCAs pool.
//!
//! The harness certificates are self-signed and marked as CAs, and the server
//! presents that very certificate. Go accepts it because the leaf is itself a
//! trusted root; webpki rejects a CA certificate as end entity. A presented
//! certificate that is byte-identical to one in the `-ca` file is therefore
//! trusted directly, after checking the server name, while any other chain
//! must validate against the `-ca` certificates as roots. The handshake
//! signature is checked in both cases.

use std::sync::Arc;

use rustls::client::danger::{HandshakeSignatureValid, ServerCertVerified, ServerCertVerifier};
use rustls::client::{Resumption, WebPkiServerVerifier};
use rustls::crypto::CryptoProvider;
use rustls::pki_types::pem::PemObject;
use rustls::pki_types::{CertificateDer, ServerName, UnixTime};
use rustls::{CertificateError, ClientConfig, DigitallySignedStruct, RootCertStore, SignatureScheme};

#[derive(Debug)]
struct CaVerifier {
    trusted: Vec<CertificateDer<'static>>,
    chains: Arc<WebPkiServerVerifier>,
}

impl ServerCertVerifier for CaVerifier {
    fn verify_server_cert(
        &self, end_entity: &CertificateDer<'_>, intermediates: &[CertificateDer<'_>],
        server_name: &ServerName<'_>, ocsp_response: &[u8], now: UnixTime,
    ) -> Result<ServerCertVerified, rustls::Error> {
        if !self.trusted.iter().any(|cert| cert == end_entity) {
            return self.chains.verify_server_cert(
                end_entity,
                intermediates,
                server_name,
                ocsp_response,
                now,
            );
        }
        webpki::EndEntityCert::try_from(end_entity)
            .map_err(|_| CertificateError::BadEncoding)?
            .verify_is_valid_for_subject_name(server_name)
            .map_err(|_| CertificateError::NotValidForName)?;
        Ok(ServerCertVerified::assertion())
    }

    fn verify_tls12_signature(
        &self, message: &[u8], cert: &CertificateDer<'_>, dss: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, rustls::Error> {
        self.chains.verify_tls12_signature(message, cert, dss)
    }

    fn verify_tls13_signature(
        &self, message: &[u8], cert: &CertificateDer<'_>, dss: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, rustls::Error> {
        self.chains.verify_tls13_signature(message, cert, dss)
    }

    fn supported_verify_schemes(&self) -> Vec<SignatureScheme> {
        self.chains.supported_verify_schemes()
    }
}

/// Builds the TLS configuration shared by all connections: TLS 1.3 with the
/// cipher suites of s2n-quic's rustls provider, the certificates in `ca_path`
/// as trust anchors, and no session resumption, so that every connection
/// performs a full handshake.
pub fn client_config(ca_path: &str, alpn: &str) -> Result<Arc<ClientConfig>, String> {
    let trusted = CertificateDer::pem_file_iter(ca_path)
        .and_then(|certs| certs.collect::<Result<Vec<_>, _>>())
        .map_err(|e| format!("read {ca_path}: {e}"))?;
    if trusted.is_empty() {
        return Err(format!("no certificate in {ca_path}"));
    }
    let mut roots = RootCertStore::empty();
    roots.add_parsable_certificates(trusted.iter().cloned());

    #[allow(deprecated)]
    let cipher_suites = s2n_quic::provider::tls::rustls::DEFAULT_CIPHERSUITES.to_vec();
    let provider =
        Arc::new(CryptoProvider { cipher_suites, ..rustls::crypto::aws_lc_rs::default_provider() });
    let chains = WebPkiServerVerifier::builder_with_provider(Arc::new(roots), provider.clone())
        .build()
        .map_err(|e| format!("{ca_path}: {e}"))?;
    let mut config = ClientConfig::builder_with_provider(provider)
        .with_protocol_versions(&[&rustls::version::TLS13])
        .map_err(|e| e.to_string())?
        .dangerous()
        .with_custom_certificate_verifier(Arc::new(CaVerifier { trusted, chains }))
        .with_no_client_auth();
    config.alpn_protocols = vec![alpn.as_bytes().to_vec()];
    config.resumption = Resumption::disabled();
    Ok(Arc::new(config))
}
