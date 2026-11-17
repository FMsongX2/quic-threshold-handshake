"""aioquic server with two certificate key modes.

With ``--key`` the key is local and every signature is delayed by a fixed
time. With ``--coordd`` the key asks a qth-coordd daemon for each signature.
aioquic calls ``certificate_private_key.sign()`` synchronously inside the
asyncio event loop, so in both modes signing blocks the loop that serves all
connections.
"""

import argparse
import asyncio
import os
import socket
import struct
import time

from aioquic.asyncio import QuicConnectionProtocol, serve
from aioquic.quic.configuration import QuicConfiguration
from aioquic.quic.logger import QuicFileLogger
from cryptography import x509
from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.primitives.asymmetric import ec, ed25519, padding, rsa


class DelayedKey:
    """Wraps an EC private key and sleeps after each signature."""

    def __init__(self, inner: ec.EllipticCurvePrivateKey, delay_s: float) -> None:
        self._inner = inner
        self._delay_s = delay_s

    @property
    def curve(self) -> ec.EllipticCurve:
        return self._inner.curve

    @property
    def key_size(self) -> int:
        return self._inner.key_size

    def public_key(self) -> ec.EllipticCurvePublicKey:
        return self._inner.public_key()

    def sign(self, data: bytes, *args) -> bytes:
        signature = self._inner.sign(data, *args)
        time.sleep(self._delay_s)
        return signature


class CoorddClient:
    """Blocking client of the qth-coordd protocol over one TCP connection.

    Requests never overlap because aioquic signs on the event loop thread, so
    each response read is the answer to the request just sent.
    """

    def __init__(self, address: str) -> None:
        host, _, port = address.rpartition(":")
        self._address = (host.strip("[]"), int(port))
        self._sock: socket.socket | None = None
        self._next_id = 0

    def sign(self, message: bytes) -> bytes:
        try:
            return self._round_trip(message)
        except OSError:
            # The daemon may have closed an idle connection; retry once on a
            # fresh one.
            self._close()
            return self._round_trip(message)

    def _round_trip(self, message: bytes) -> bytes:
        if self._sock is None:
            self._sock = socket.create_connection(self._address)
            self._sock.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
        self._next_id += 1
        request_id = self._next_id
        self._sock.sendall(struct.pack(">IQ", 8 + len(message), request_id) + message)
        (length,) = struct.unpack(">I", self._read_exact(4))
        body = self._read_exact(length)
        if len(body) < 9:
            raise ConnectionError("coordd: short response")
        (response_id,) = struct.unpack(">Q", body[:8])
        if response_id != request_id:
            raise ConnectionError(f"coordd: response {response_id} for request {request_id}")
        if body[8] != 0:
            raise ValueError("coordd: daemon error: " + body[9:].decode(errors="replace"))
        return body[9:]

    def _read_exact(self, size: int) -> bytes:
        chunks = []
        while size:
            chunk = self._sock.recv(size)
            if not chunk:
                raise ConnectionError("coordd: connection closed")
            chunks.append(chunk)
            size -= len(chunk)
        return b"".join(chunks)

    def _close(self) -> None:
        if self._sock is not None:
            self._sock.close()
            self._sock = None


class CoorddKey:
    """Certificate key whose signatures come from qth-coordd.

    The daemon signs with the one TLS 1.3 scheme the key type implies, so the
    arguments aioquic passes are only checked against that scheme.
    """

    def __init__(self, client: CoorddClient, public: object) -> None:
        self._client = client
        self._public = public

    def public_key(self) -> object:
        return self._public

    def _check(self, args: tuple) -> None:
        raise NotImplementedError

    def sign(self, data: bytes, *args) -> bytes:
        self._check(args)
        return self._client.sign(data)


class CoorddEd25519Key(CoorddKey):
    def _check(self, args: tuple) -> None:
        if args:
            raise ValueError("Ed25519 signs without parameters")


class CoorddRSAKey(CoorddKey):
    @property
    def key_size(self) -> int:
        return self._public.key_size

    def _check(self, args: tuple) -> None:
        if len(args) != 2 or not isinstance(args[0], padding.PSS) or not isinstance(args[1], hashes.SHA256):
            raise ValueError("coordd RSA keys support only rsa_pss_rsae_sha256")


class CoorddECKey(CoorddKey):
    @property
    def curve(self) -> ec.EllipticCurve:
        return self._public.curve

    @property
    def key_size(self) -> int:
        return self._public.key_size

    def _check(self, args: tuple) -> None:
        if len(args) != 1 or not isinstance(args[0].algorithm, hashes.SHA256):
            raise ValueError("coordd EC keys support only ecdsa_secp256r1_sha256")


# aioquic selects the signature scheme with isinstance checks on the key.
ec.EllipticCurvePrivateKey.register(DelayedKey)
ed25519.Ed25519PrivateKey.register(CoorddEd25519Key)
rsa.RSAPrivateKey.register(CoorddRSAKey)
ec.EllipticCurvePrivateKey.register(CoorddECKey)


def coordd_key(address: str, certificate: x509.Certificate) -> CoorddKey:
    public = certificate.public_key()
    client = CoorddClient(address)
    if isinstance(public, ed25519.Ed25519PublicKey):
        return CoorddEd25519Key(client, public)
    if isinstance(public, rsa.RSAPublicKey):
        return CoorddRSAKey(client, public)
    if isinstance(public, ec.EllipticCurvePublicKey) and isinstance(public.curve, ec.SECP256R1):
        return CoorddECKey(client, public)
    raise SystemExit(f"unsupported certificate key {type(public).__name__}")


async def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", default="0.0.0.0")
    parser.add_argument("--port", type=int, default=4433)
    parser.add_argument("--cert", required=True)
    keys = parser.add_mutually_exclusive_group(required=True)
    keys.add_argument("--key", help="PEM EC private key (local key mode)")
    keys.add_argument("--coordd", help="qth-coordd HOST:PORT that signs for the key in --cert")
    parser.add_argument("--alpn", default="qth")
    parser.add_argument("--sign-delay-ms", type=int, default=0, help="time added to every signature (local key mode)")
    args = parser.parse_args()

    qlog_dir = os.environ.get("QLOGDIR")
    configuration = QuicConfiguration(
        is_client=False,
        alpn_protocols=[args.alpn],
        quic_logger=QuicFileLogger(qlog_dir) if qlog_dir else None,
    )
    if args.coordd:
        with open(args.cert, "rb") as f:
            certificates = x509.load_pem_x509_certificates(f.read())
        configuration.certificate = certificates[0]
        configuration.certificate_chain = certificates[1:]
        configuration.private_key = coordd_key(args.coordd, certificates[0])
        mode = "coordd"
    else:
        configuration.load_cert_chain(args.cert, args.key)
        if not isinstance(configuration.private_key, ec.EllipticCurvePrivateKey):
            raise SystemExit("only EC private keys are supported")
        configuration.private_key = DelayedKey(configuration.private_key, args.sign_delay_ms / 1000)
        mode = "local"

    await serve(args.host, args.port, configuration=configuration, create_protocol=QuicConnectionProtocol)
    print(f"ready listen={args.host}:{args.port} sign_delay_ms={args.sign_delay_ms} key={mode}", flush=True)
    await asyncio.Future()


if __name__ == "__main__":
    asyncio.run(main())
