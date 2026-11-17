//! Works around a lost wake-up in s2n-quic's TLS offloading (s2n-quic 1.92).
//!
//! The QUIC side of an offloaded session reads the TLS task's requests from a
//! channel whose receiver registers a waker only when it finds the channel
//! empty. After the TLS task delivers the ServerHello and the handshake keys,
//! the QUIC side drains them and reports that the Handshake space can now
//! carry data; the TLS task then sends the rest of its flight, but no waker
//! is registered for it, so the flight waits for the next datagram from the
//! client, one round trip later. Polling the session once more whenever it
//! returns pending finds the channel empty and registers the waker, or picks
//! up requests that arrived in between.

use std::task::Poll;

use s2n_quic::provider::tls::Provider;
use s2n_quic_core::application::ServerName;
use s2n_quic_core::crypto::tls::{self, ApplicationParameters, Bytes, NamedGroup, TlsSession};
use s2n_quic_core::crypto::CryptoSuite;
use s2n_quic_core::transport;

pub struct Rearm<P>(pub P);

impl<P: Provider> Provider for Rearm<P> {
    type Server = Endpoint<P::Server>;
    type Client = Endpoint<P::Client>;
    type Error = P::Error;

    fn start_server(self) -> Result<Self::Server, Self::Error> {
        self.0.start_server().map(Endpoint)
    }

    fn start_client(self) -> Result<Self::Client, Self::Error> {
        self.0.start_client().map(Endpoint)
    }
}

pub struct Endpoint<E>(E);

impl<E: tls::Endpoint> tls::Endpoint for Endpoint<E> {
    type Session = Session<E::Session>;

    fn new_server_session<Params: s2n_codec::EncoderValue>(
        &mut self, transport_parameters: &Params, connection_info: tls::ConnectionInfo,
    ) -> Self::Session {
        Session(self.0.new_server_session(transport_parameters, connection_info))
    }

    fn new_client_session<Params: s2n_codec::EncoderValue>(
        &mut self, transport_parameters: &Params, server_name: ServerName,
    ) -> Self::Session {
        Session(self.0.new_client_session(transport_parameters, server_name))
    }

    fn max_tag_length(&self) -> usize {
        self.0.max_tag_length()
    }
}

#[derive(Debug)]
pub struct Session<S>(S);

impl<S: tls::Session> CryptoSuite for Session<S> {
    type HandshakeKey = S::HandshakeKey;
    type HandshakeHeaderKey = S::HandshakeHeaderKey;
    type InitialKey = S::InitialKey;
    type InitialHeaderKey = S::InitialHeaderKey;
    type OneRttKey = S::OneRttKey;
    type OneRttHeaderKey = S::OneRttHeaderKey;
    type ZeroRttKey = S::ZeroRttKey;
    type ZeroRttHeaderKey = S::ZeroRttHeaderKey;
    type RetryKey = S::RetryKey;
}

impl<S: tls::Session> tls::Session for Session<S> {
    fn poll<C: tls::Context<Self>>(
        &mut self, context: &mut C,
    ) -> Poll<Result<(), transport::Error>> {
        let mut context = Forward(context);
        match self.0.poll(&mut context) {
            Poll::Pending => self.0.poll(&mut context),
            ready => ready,
        }
    }

    fn process_post_handshake_message<C: tls::Context<Self>>(
        &mut self, context: &mut C,
    ) -> Result<(), transport::Error> {
        self.0.process_post_handshake_message(&mut Forward(context))
    }

    fn should_discard_session(&self) -> bool {
        self.0.should_discard_session()
    }

    fn parse_hello(
        msg_type: tls::HandshakeType, header_chunk: &[u8], total_received_len: u64,
        max_hello_size: u64,
    ) -> Result<Option<tls::HelloOffsets>, transport::Error> {
        S::parse_hello(msg_type, header_chunk, total_received_len, max_hello_size)
    }
}

/// Presents the context of the wrapping session to the wrapped one.
struct Forward<'a, C>(&'a mut C);

impl<S: tls::Session, C: tls::Context<Session<S>>> tls::Context<S> for Forward<'_, C> {
    fn on_client_application_params(
        &mut self, client_params: ApplicationParameters, server_params: &mut Vec<u8>,
    ) -> Result<(), transport::Error> {
        self.0.on_client_application_params(client_params, server_params)
    }

    fn on_handshake_keys(
        &mut self, key: S::HandshakeKey, header_key: S::HandshakeHeaderKey,
    ) -> Result<(), transport::Error> {
        self.0.on_handshake_keys(key, header_key)
    }

    fn on_zero_rtt_keys(
        &mut self, key: S::ZeroRttKey, header_key: S::ZeroRttHeaderKey,
        application_parameters: ApplicationParameters,
    ) -> Result<(), transport::Error> {
        self.0.on_zero_rtt_keys(key, header_key, application_parameters)
    }

    fn on_one_rtt_keys(
        &mut self, key: S::OneRttKey, header_key: S::OneRttHeaderKey,
        application_parameters: ApplicationParameters,
    ) -> Result<(), transport::Error> {
        self.0.on_one_rtt_keys(key, header_key, application_parameters)
    }

    fn on_server_name(&mut self, server_name: ServerName) -> Result<(), transport::Error> {
        self.0.on_server_name(server_name)
    }

    fn on_application_protocol(
        &mut self, application_protocol: Bytes,
    ) -> Result<(), transport::Error> {
        self.0.on_application_protocol(application_protocol)
    }

    fn on_key_exchange_group(&mut self, named_group: NamedGroup) -> Result<(), transport::Error> {
        self.0.on_key_exchange_group(named_group)
    }

    fn on_handshake_complete(&mut self) -> Result<(), transport::Error> {
        self.0.on_handshake_complete()
    }

    fn on_tls_context(&mut self, context: Box<dyn std::any::Any + Send>) {
        self.0.on_tls_context(context)
    }

    fn on_tls_exporter_ready(&mut self, session: &impl TlsSession) -> Result<(), transport::Error> {
        self.0.on_tls_exporter_ready(session)
    }

    fn on_tls_handshake_failed(
        &mut self, session: &impl TlsSession,
        error: Box<dyn std::error::Error + Send + Sync + 'static>,
    ) -> Result<(), transport::Error> {
        self.0.on_tls_handshake_failed(session, error)
    }

    fn receive_initial(&mut self, max_len: Option<usize>) -> Option<Bytes> {
        self.0.receive_initial(max_len)
    }

    fn receive_handshake(&mut self, max_len: Option<usize>) -> Option<Bytes> {
        self.0.receive_handshake(max_len)
    }

    fn receive_application(&mut self, max_len: Option<usize>) -> Option<Bytes> {
        self.0.receive_application(max_len)
    }

    fn can_send_initial(&self) -> bool {
        self.0.can_send_initial()
    }

    fn send_initial(&mut self, transmission: Bytes) {
        self.0.send_initial(transmission)
    }

    fn can_send_handshake(&self) -> bool {
        self.0.can_send_handshake()
    }

    fn send_handshake(&mut self, transmission: Bytes) {
        self.0.send_handshake(transmission)
    }

    fn can_send_application(&self) -> bool {
        self.0.can_send_application()
    }

    fn send_application(&mut self, transmission: Bytes) {
        self.0.send_application(transmission)
    }

    fn waker(&self) -> &std::task::Waker {
        self.0.waker()
    }
}
