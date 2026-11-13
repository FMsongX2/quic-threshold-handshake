FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
RUN CGO_ENABLED=0 go build -o /out/qth-client ./cmd/client \
 && CGO_ENABLED=0 go build -o /out/qth-server-quicgo ./cmd/server-quicgo

FROM alpine:3.20
RUN apk add --no-cache iproute2-tc
COPY --from=build /out/ /usr/local/bin/
