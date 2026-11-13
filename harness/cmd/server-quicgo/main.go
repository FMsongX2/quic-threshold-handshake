// Command qth-server-quicgo is a quic-go server with two certificate key
// modes. With -key the key is local and every signature is delayed by a fixed
// time. With -threshold-public the key is a tsig.Coordinator that obtains
// signatures from remote signers. In both modes crypto.Signer is synchronous,
// so signing blocks the goroutine that drives the TLS handshake of that
// connection.
package main

import (
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"flag"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/qlog"

	"github.com/FMsongX2/quic-threshold-handshake/harness/internal/tsig"
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
	publicPath := flag.String("threshold-public", "", "public.json written by qth-dealer (remote signer mode)")
	signers := flag.String("signers", "", "comma-separated signer addresses in ID order")
	policy := flag.String("policy", tsig.PolicyFixed, "fixed, fastest, first or hedge")
	hedgeWidth := flag.Int("hedge-width", 0, "signers engaged by the hedge policy; 0 means all")
	hedgeDelay := flag.Float64("hedge-delay-factor", 0, "start with t signers and widen after this multiple of their expected response time; 0 widens at once")
	hedgeBudget := flag.Float64("hedge-budget", 0, "largest share of signatures that may widen after the delay; 0 means no limit")
	nudgeDelay := flag.Float64("nudge-delay-factor", 0, "ping signers still silent after this multiple of the expected round trip; 0 disables")
	transport := flag.String("transport", tsig.TransportPersistent, "persistent or per-request")
	precompute := flag.Bool("precompute", false, "collect FROST commitments ahead of time")
	tracePath := flag.String("trace-out", "", "JSON lines file for per-signature traces")
	flag.Parse()

	var cert tls.Certificate
	if *publicPath == "" {
		var err error
		if cert, err = tls.LoadX509KeyPair(*certPath, *keyPath); err != nil {
			log.Fatal(err)
		}
		signer, ok := cert.PrivateKey.(crypto.Signer)
		if !ok {
			log.Fatal("private key does not implement crypto.Signer")
		}
		cert.PrivateKey = delayedSigner{Signer: signer, delay: time.Duration(*delayMs) * time.Millisecond}
	} else {
		cert = thresholdCertificate(*certPath, *publicPath, *signers, tsig.Options{
			Policy: *policy, HedgeWidth: *hedgeWidth, HedgeDelayFactor: *hedgeDelay, HedgeBudget: *hedgeBudget,
			NudgeDelayFactor: *nudgeDelay, Transport: *transport, Precompute: *precompute,
			OnTrace: traceWriter(*tracePath),
		})
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
	log.Printf("ready listen=%s sign_delay_ms=%d threshold=%t", *listen, *delayMs, *publicPath != "")

	for {
		conn, err := ln.Accept(context.Background())
		if err != nil {
			log.Fatal(err)
		}
		go func() { <-conn.Context().Done() }()
	}
}

func thresholdCertificate(certPath, publicPath, signers string, opts tsig.Options) tls.Certificate {
	pub, err := tsig.LoadPublic(publicPath)
	if err != nil {
		log.Fatal(err)
	}
	coordinator, err := tsig.NewCoordinator(pub, strings.Split(signers, ","), opts)
	if err != nil {
		log.Fatal(err)
	}
	raw, err := os.ReadFile(certPath)
	if err != nil {
		log.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		log.Fatalf("no certificate in %s", certPath)
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		log.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{block.Bytes}, PrivateKey: coordinator, Leaf: leaf}
	if pub.Scheme == tsig.SchemeRSA || pub.Scheme == tsig.SchemePlainRSA {
		// The signers implement only this RSA scheme.
		cert.SupportedSignatureAlgorithms = []tls.SignatureScheme{tls.PSSWithSHA256}
	}
	return cert
}

// traceWriter returns a callback that appends traces to path, or nil.
func traceWriter(path string) func(*tsig.Trace) {
	if path == "" {
		return nil
	}
	file, err := os.Create(path)
	if err != nil {
		log.Fatal(err)
	}
	var mu sync.Mutex
	enc := json.NewEncoder(file)
	return func(trace *tsig.Trace) {
		mu.Lock()
		defer mu.Unlock()
		enc.Encode(trace)
	}
}
