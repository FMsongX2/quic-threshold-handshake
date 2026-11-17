FROM rust:1-bookworm AS build
RUN apt-get update \
 && apt-get install -y --no-install-recommends cmake clang libclang-dev \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /src
# Only sources and lockfiles, so that local target directories stay out.
COPY servers/s2n-quic/Cargo.toml servers/s2n-quic/Cargo.lock servers/s2n-quic/
COPY servers/s2n-quic/src/ servers/s2n-quic/src/
COPY clients/s2n-quic/Cargo.toml clients/s2n-quic/Cargo.lock clients/s2n-quic/
COPY clients/s2n-quic/src/ clients/s2n-quic/src/
# One target directory lets both crates reuse the AWS-LC build.
ENV CARGO_TARGET_DIR=/src/target
RUN cargo build --release --locked --manifest-path servers/s2n-quic/Cargo.toml \
 && cargo build --release --locked --manifest-path clients/s2n-quic/Cargo.toml

FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends iproute2 \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /src/target/release/qth-server-s2n /src/target/release/qth-client-s2n /usr/local/bin/
