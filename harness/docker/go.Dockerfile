FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN for name in client server-quicgo dealer signer signbench; do \
      CGO_ENABLED=0 go build -o /out/qth-$name ./cmd/$name || exit 1; \
    done

FROM alpine:3.20
RUN apk add --no-cache iproute2-tc
COPY --from=build /out/ /usr/local/bin/
