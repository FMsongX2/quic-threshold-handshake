//! Client of the qth-coordd signing daemon, for either of its listeners: the
//! message listener signs the unhashed CertificateVerify input, the digest
//! listener signs its SHA-256 digest. Both use the same framing.
//!
//! All handshakes share one TCP connection and keep their requests in flight
//! concurrently; a broken connection fails its pending requests and is
//! replaced by the next request.

use std::collections::HashMap;
use std::fmt;
use std::future::Future;
use std::io::{Read, Write};
use std::net::{Shutdown, TcpStream};
use std::pin::Pin;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{mpsc, Arc, Mutex};
use std::task::{Context, Poll};
use std::time::Duration;

use tokio::sync::oneshot;

/// Longest a handshake waits for one signature before failing it.
const SIGN_TIMEOUT: Duration = Duration::from_secs(30);
/// Bound on a response frame, far above any signature the daemon returns.
const MAX_FRAME: usize = 1 << 20;

type Signature = Result<Vec<u8>, String>;
type Reply = Box<dyn FnOnce(Signature) + Send>;

pub struct Daemon {
    addr: String,
    next_id: AtomicU64,
    link: Mutex<Option<Arc<Link>>>,
}

struct Link {
    writer: Mutex<TcpStream>,
    /// Waiting requests by id; `None` once the connection has failed.
    pending: Mutex<Option<HashMap<u64, Reply>>>,
}

impl fmt::Debug for Daemon {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Daemon").field("addr", &self.addr).finish()
    }
}

impl Daemon {
    /// Connects at once so that an unreachable daemon stops the server at startup.
    pub fn connect(addr: &str) -> Result<Arc<Self>, String> {
        let daemon =
            Arc::new(Self { addr: addr.into(), next_id: AtomicU64::new(0), link: Mutex::new(None) });
        daemon.link()?;
        Ok(daemon)
    }

    fn link(&self) -> Result<Arc<Link>, String> {
        let mut slot = self.link.lock().unwrap();
        if let Some(link) = slot.as_ref().filter(|link| link.is_open()) {
            return Ok(link.clone());
        }
        let stream =
            TcpStream::connect(&self.addr).map_err(|e| format!("connect {}: {e}", self.addr))?;
        stream.set_nodelay(true).map_err(|e| e.to_string())?;
        let reader = stream.try_clone().map_err(|e| e.to_string())?;
        let link = Arc::new(Link {
            writer: Mutex::new(stream),
            pending: Mutex::new(Some(HashMap::new())),
        });
        let receiver = link.clone();
        std::thread::spawn(move || receiver.read_responses(reader));
        *slot = Some(link.clone());
        Ok(link)
    }

    /// Sends one request; `reply` receives its outcome exactly once unless
    /// the request is withdrawn.
    fn submit(&self, input: &[u8], reply: Reply) -> Result<Request, String> {
        let link = self.link()?;
        let id = self.next_id.fetch_add(1, Ordering::Relaxed);
        link.pending
            .lock()
            .unwrap()
            .as_mut()
            .ok_or("daemon connection closed")?
            .insert(id, reply);

        let body_len = u32::try_from(8 + input.len()).map_err(|_| "input too long")?;
        let mut frame = Vec::with_capacity(12 + input.len());
        frame.extend_from_slice(&body_len.to_be_bytes());
        frame.extend_from_slice(&id.to_be_bytes());
        frame.extend_from_slice(input);
        let written = link.writer.lock().unwrap().write_all(&frame);
        if let Err(e) = written {
            link.fail(&format!("daemon write: {e}"));
        }
        Ok(Request { link, id })
    }

    /// Blocks until the daemon returns its signature over `input`.
    pub fn sign(&self, input: &[u8]) -> Signature {
        let (reply, response) = mpsc::channel();
        let request = self.submit(
            input,
            Box::new(move |signature| {
                let _ = reply.send(signature);
            }),
        )?;
        response.recv_timeout(SIGN_TIMEOUT).unwrap_or_else(|_| {
            request.withdraw();
            Err(format!("no daemon response within {SIGN_TIMEOUT:?}"))
        })
    }

    /// Returns a future of the daemon's signature over `input`. The request
    /// is sent at once.
    pub fn sign_async(&self, input: &[u8]) -> PendingSignature {
        let (reply, response) = oneshot::channel();
        let request = self.submit(
            input,
            Box::new(move |signature| {
                let _ = reply.send(signature);
            }),
        );
        PendingSignature(match request {
            Ok(request) => Pending::Waiting {
                request,
                response,
                deadline: Box::pin(tokio::time::sleep(SIGN_TIMEOUT)),
            },
            Err(e) => Pending::Failed(Some(e)),
        })
    }
}

struct Request {
    link: Arc<Link>,
    id: u64,
}

impl Request {
    fn withdraw(&self) {
        if let Some(pending) = self.link.pending.lock().unwrap().as_mut() {
            pending.remove(&self.id);
        }
    }
}

pub struct PendingSignature(Pending);

enum Pending {
    Waiting {
        request: Request,
        response: oneshot::Receiver<Signature>,
        deadline: Pin<Box<tokio::time::Sleep>>,
    },
    Failed(Option<String>),
}

impl Future for PendingSignature {
    type Output = Signature;

    fn poll(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Signature> {
        match &mut self.0 {
            Pending::Failed(error) => {
                Poll::Ready(Err(error.take().expect("polled after completion")))
            },
            Pending::Waiting { request, response, deadline } => {
                if let Poll::Ready(result) = Pin::new(response).poll(cx) {
                    return Poll::Ready(result.unwrap_or_else(|_| Err("reply dropped".into())));
                }
                if deadline.as_mut().poll(cx).is_ready() {
                    request.withdraw();
                    return Poll::Ready(Err(format!("no daemon response within {SIGN_TIMEOUT:?}")));
                }
                Poll::Pending
            },
        }
    }
}

impl Link {
    fn is_open(&self) -> bool {
        self.pending.lock().unwrap().is_some()
    }

    fn read_responses(&self, mut stream: TcpStream) {
        let error = loop {
            match read_response(&mut stream) {
                Ok((id, result)) => {
                    let reply = self.pending.lock().unwrap().as_mut().and_then(|p| p.remove(&id));
                    if let Some(reply) = reply {
                        reply(result);
                    }
                },
                Err(e) => break e,
            }
        };
        self.fail(&format!("daemon connection: {error}"));
    }

    fn fail(&self, reason: &str) {
        let pending = self.pending.lock().unwrap().take();
        for reply in pending.into_iter().flat_map(HashMap::into_values) {
            reply(Err(reason.into()));
        }
        // Wakes the reader thread when the failure was seen by a writer.
        let _ = self.writer.lock().unwrap().shutdown(Shutdown::Both);
    }
}

fn read_response(stream: &mut TcpStream) -> Result<(u64, Signature), String> {
    let mut len = [0; 4];
    stream.read_exact(&mut len).map_err(|e| e.to_string())?;
    let len = u32::from_be_bytes(len) as usize;
    if !(9..=MAX_FRAME).contains(&len) {
        return Err(format!("invalid response length {len}"));
    }
    let mut body = vec![0; len];
    stream.read_exact(&mut body).map_err(|e| e.to_string())?;
    let id = u64::from_be_bytes(body[..8].try_into().unwrap());
    let payload = body[9..].to_vec();
    let result = match body[8] {
        0 => Ok(payload),
        1 => Err(format!("daemon: {}", String::from_utf8_lossy(&payload))),
        status => Err(format!("daemon returned unknown status {status}")),
    };
    Ok((id, result))
}
