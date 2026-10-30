//! dyno bench server: Rust, hyper 1.x on tokio.
//! RT=mt (default, multi-thread, one worker per allowed CPU), RT=ct (current-thread),
//! RT_WORKERS=n overrides the multi-thread worker count.
use std::convert::Infallible;
use std::pin::Pin;
use std::sync::OnceLock;
use std::task::{Context, Poll};
use std::time::Duration;

use bytes::Bytes;
use http::{Method, Request, Response, StatusCode, header};
use http_body::{Body, Frame, SizeHint};
use http_body_util::{BodyExt, Either, Full};
use hyper::body::Incoming;
use hyper::server::conn::http1;
use hyper::service::service_fn;
use hyper_util::rt::{TokioIo, TokioTimer};
use tokio::net::TcpListener;

const PAYLOAD_SIZE: usize = 1 << 20; // 1 MiB of random bytes, generated once
const WRITE_CHUNK: usize = 256 << 10; // bytes handed to hyper per frame
const MAX_DOWNLOAD: u64 = 1 << 34;
static INDEX_HTML: &[u8] = include_bytes!("../../../static/index.html");
static PAYLOAD: OnceLock<Vec<u8>> = OnceLock::new();

/// Streams `remaining` bytes by slicing the shared payload; no payload allocation per request.
struct Download {
    remaining: u64,
    off: usize,
}

impl Body for Download {
    type Data = Bytes;
    type Error = Infallible;

    fn poll_frame(mut self: Pin<&mut Self>, _: &mut Context<'_>) -> Poll<Option<Result<Frame<Bytes>, Infallible>>> {
        if self.remaining == 0 {
            return Poll::Ready(None);
        }
        let p: &'static [u8] = PAYLOAD.get().unwrap();
        let c = WRITE_CHUNK.min(PAYLOAD_SIZE - self.off).min(self.remaining as usize);
        let chunk = Bytes::from_static(&p[self.off..self.off + c]);
        self.remaining -= c as u64;
        self.off = (self.off + c) % PAYLOAD_SIZE;
        Poll::Ready(Some(Ok(Frame::data(chunk))))
    }

    fn is_end_stream(&self) -> bool {
        self.remaining == 0
    }

    fn size_hint(&self) -> SizeHint {
        SizeHint::with_exact(self.remaining)
    }
}

type Out = Either<Download, Full<Bytes>>;

fn small(status: StatusCode, ctype: &'static str, body: Bytes) -> Response<Out> {
    Response::builder()
        .status(status)
        .header(header::CONTENT_TYPE, ctype)
        .body(Either::Right(Full::new(body)))
        .unwrap()
}

fn query_bytes(q: Option<&str>) -> Option<u64> {
    q?.split('&').find_map(|kv| kv.strip_prefix("bytes=")).and_then(|v| v.parse().ok())
}

async fn handle(req: Request<Incoming>) -> Result<Response<Out>, Infallible> {
    let path = req.uri().path();
    Ok(match (req.method(), path) {
        (&Method::GET, "/dl") => match query_bytes(req.uri().query()) {
            Some(n) if n <= MAX_DOWNLOAD => Response::builder()
                .header(header::CONTENT_TYPE, "application/octet-stream")
                .header(header::CACHE_CONTROL, "no-store")
                .header(header::CONTENT_LENGTH, n)
                .body(Either::Left(Download { remaining: n, off: 0 }))
                .unwrap(),
            _ => small(StatusCode::BAD_REQUEST, "text/plain", Bytes::from_static(b"bad bytes\n")),
        },
        (&Method::POST, "/ul") => {
            let mut body = req.into_body();
            let mut total: u64 = 0;
            while let Some(frame) = body.frame().await {
                match frame {
                    Ok(f) => {
                        if let Some(d) = f.data_ref() {
                            total += d.len() as u64;
                        }
                    }
                    Err(_) => break,
                }
            }
            small(StatusCode::OK, "text/plain", Bytes::from(total.to_string()))
        }
        (&Method::GET, "/ping") => Response::builder()
            .status(StatusCode::NO_CONTENT)
            .header(header::CACHE_CONTROL, "no-store")
            .body(Either::Right(Full::new(Bytes::new())))
            .unwrap(),
        (&Method::GET, "/") => small(StatusCode::OK, "text/html; charset=utf-8", Bytes::from_static(INDEX_HTML)),
        _ => small(StatusCode::NOT_FOUND, "text/plain", Bytes::from_static(b"not found\n")),
    })
}

async fn serve(port: u16) {
    let listener = TcpListener::bind(("127.0.0.1", port)).await.expect("bind");
    loop {
        let (stream, _) = match listener.accept().await {
            Ok(s) => s,
            Err(_) => continue,
        };
        let _ = stream.set_nodelay(true);
        tokio::spawn(async move {
            let _ = http1::Builder::new()
                .timer(TokioTimer::new())
                .header_read_timeout(Duration::from_secs(10))
                .serve_connection(TokioIo::new(stream), service_fn(handle))
                .await;
        });
    }
}

fn main() {
    let mut p = vec![0u8; PAYLOAD_SIZE];
    use std::io::Read;
    std::fs::File::open("/dev/urandom").and_then(|mut f| f.read_exact(&mut p)).expect("urandom");
    PAYLOAD.set(p).unwrap();

    let port: u16 = std::env::var("PORT").ok().and_then(|v| v.parse().ok()).unwrap_or(8080);
    let rt = match std::env::var("RT").as_deref() {
        Ok("ct") => tokio::runtime::Builder::new_current_thread().enable_all().build(),
        _ => {
            let mut b = tokio::runtime::Builder::new_multi_thread();
            if let Some(n) = std::env::var("RT_WORKERS").ok().and_then(|v| v.parse().ok()) {
                b.worker_threads(n);
            }
            b.enable_all().build()
        }
    }
    .expect("runtime");
    rt.block_on(serve(port));
}
