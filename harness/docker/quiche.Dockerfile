FROM golang:1.26-alpine AS coordd
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -o /out/qth-coordd ./cmd/coordd

FROM rust:1-bookworm AS build
RUN apt-get update \
 && apt-get install -y --no-install-recommends cmake clang libclang-dev \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY servers/quiche/ .
RUN cargo build --release --locked

FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends iproute2 \
 && rm -rf /var/lib/apt/lists/*
COPY --from=coordd /out/qth-coordd /usr/local/bin/
COPY docker/serve-with-coordd /usr/local/bin/
COPY --from=build /src/target/release/qth-server-quiche /usr/local/bin/
