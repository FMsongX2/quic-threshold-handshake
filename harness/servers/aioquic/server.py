"""aioquic server whose certificate key delays every signature by a fixed time.

aioquic calls ``certificate_private_key.sign()`` synchronously inside the
asyncio event loop, so the delay blocks the loop that serves all connections.
"""

import argparse
import asyncio
import os
import time

from aioquic.asyncio import QuicConnectionProtocol, serve
from aioquic.quic.configuration import QuicConfiguration
from aioquic.quic.logger import QuicFileLogger
from cryptography.hazmat.primitives.asymmetric import ec


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


# aioquic selects the signature scheme with isinstance checks on the key.
ec.EllipticCurvePrivateKey.register(DelayedKey)


async def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", default="0.0.0.0")
    parser.add_argument("--port", type=int, default=4433)
    parser.add_argument("--cert", required=True)
    parser.add_argument("--key", required=True)
    parser.add_argument("--alpn", default="qth")
    parser.add_argument("--sign-delay-ms", type=int, default=0)
    args = parser.parse_args()

    qlog_dir = os.environ.get("QLOGDIR")
    configuration = QuicConfiguration(
        is_client=False,
        alpn_protocols=[args.alpn],
        quic_logger=QuicFileLogger(qlog_dir) if qlog_dir else None,
    )
    configuration.load_cert_chain(args.cert, args.key)
    if not isinstance(configuration.private_key, ec.EllipticCurvePrivateKey):
        raise SystemExit("only EC private keys are supported")
    configuration.private_key = DelayedKey(configuration.private_key, args.sign_delay_ms / 1000)

    await serve(args.host, args.port, configuration=configuration, create_protocol=QuicConnectionProtocol)
    print(f"ready listen={args.host}:{args.port} sign_delay_ms={args.sign_delay_ms}", flush=True)
    await asyncio.Future()


if __name__ == "__main__":
    asyncio.run(main())
