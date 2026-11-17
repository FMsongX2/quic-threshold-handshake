// Command qth-server-quicgo is a quic-go server with three certificate key
// modes. With -key the key is local and every signature is delayed by a fixed
// time. With -threshold-public the key is a tsig.Coordinator that obtains
// signatures from remote signers. With -coordd the key asks a qth-coordd
// daemon, which runs the coordinator in another process. In every mode
// crypto.Signer is synchronous, so signing blocks the goroutine that drives
// the TLS handshake of that connection.
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

	"github.com/FMsongX2/quic-threshold-handshake/harness/internal/coordd"
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
	keyPath := flag.String("key", "", "PEM private key (local key mode)")
	alpn := flag.String("alpn", "qth", "ALPN protocol")
	delayMs := flag.Int("sign-delay-ms", 0, "time added to every signature (local key mode)")
	coorddAddr := flag.String("coordd", "", "qth-coordd address that signs for the key in -cert (daemon mode)")
	coordFlags := coordd.RegisterCoordinatorFlags(flag.CommandLine)
	flag.Parse()

	var cert tls.Certificate
	var mode string
	switch {
	case *coorddAddr != "" && coordFlags.PublicPath != "":
		log.Fatal("-coordd and -threshold-public are exclusive")
	case *coorddAddr != "":
		mode = "coordd"
		leaf, err := coordd.LoadLeaf(*certPath)
		if err != nil {
			log.Fatal(err)
		}
		cert = coordd.Certificate(leaf, coordd.NewClient(*coorddAddr, leaf.PublicKey))
	case coordFlags.PublicPath != "":
		mode = "threshold"
		leaf, err := coordd.LoadLeaf(*certPath)
		if err != nil {
			log.Fatal(err)
		}
		coordinator, err := coordFlags.Coordinator()
		if err != nil {
			log.Fatal(err)
		}
		cert = coordd.Certificate(leaf, coordinator)
	default:
		mode = "local"
		var err error
		if cert, err = tls.LoadX509KeyPair(*certPath, *keyPath); err != nil {
			log.Fatal(err)
		}
		signer, ok := cert.PrivateKey.(crypto.Signer)
		if !ok {
			log.Fatal("private key does not implement crypto.Signer")
		}
		cert.PrivateKey = delayedSigner{Signer: signer, delay: time.Duration(*delayMs) * time.Millisecond}
	}

	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{*alpn},
		MinVersion:   tls.VersionTLS13,
	}
	ln, err := quic.ListenAddr(*listen, tlsConf, &quic.Config{Tracer: qlog.DefaultConnectionTracer})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("ready listen=%s sign_delay_ms=%d key=%s", *listen, *delayMs, mode)

	for {
		conn, err := ln.Accept(context.Background())
		if err != nil {
			log.Fatal(err)
		}
		go func() { <-conn.Context().Done() }()
	}
}
