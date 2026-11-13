// Command qth-server-quicgo is a quic-go server whose certificate key delays
// every signature by a fixed time. crypto.Signer is synchronous, so the delay
// blocks the goroutine that drives the TLS handshake of that connection.
package main

import (
	"context"
	"crypto"
	"crypto/tls"
	"flag"
	"io"
	"log"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/qlog"
)

type delayedSigner struct {
	crypto.Signer
	delay time.Duration
}

func (s delayedSigner) Sign(rand io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	sig, err := s.Signer.Sign(rand, digest, opts)
	time.Sleep(s.delay)
	return sig, err
}

func main() {
	listen := flag.String("listen", ":4433", "UDP listen address")
	certPath := flag.String("cert", "", "PEM certificate chain")
	keyPath := flag.String("key", "", "PEM private key")
	alpn := flag.String("alpn", "qth", "ALPN protocol")
	delayMs := flag.Int("sign-delay-ms", 0, "time added to every signature")
	flag.Parse()

	cert, err := tls.LoadX509KeyPair(*certPath, *keyPath)
	if err != nil {
		log.Fatal(err)
	}
	signer, ok := cert.PrivateKey.(crypto.Signer)
	if !ok {
		log.Fatal("private key does not implement crypto.Signer")
	}
	cert.PrivateKey = delayedSigner{Signer: signer, delay: time.Duration(*delayMs) * time.Millisecond}

	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{*alpn},
		MinVersion:   tls.VersionTLS13,
	}
	ln, err := quic.ListenAddr(*listen, tlsConf, &quic.Config{Tracer: qlog.DefaultConnectionTracer})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("ready listen=%s sign_delay_ms=%d", *listen, *delayMs)

	for {
		conn, err := ln.Accept(context.Background())
		if err != nil {
			log.Fatal(err)
		}
		go func() { <-conn.Context().Done() }()
	}
}
