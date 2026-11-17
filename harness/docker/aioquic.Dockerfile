FROM golang:1.26-alpine AS coordd
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -o /out/qth-coordd ./cmd/coordd

FROM python:3.12-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends iproute2 \
 && rm -rf /var/lib/apt/lists/* \
 && pip install --no-cache-dir aioquic==1.8.0
COPY --from=coordd /out/qth-coordd /usr/local/bin/
COPY docker/serve-with-coordd /usr/local/bin/
COPY servers/aioquic/server.py /opt/qth/server.py
