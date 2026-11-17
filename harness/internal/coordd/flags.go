package coordd

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/FMsongX2/quic-threshold-handshake/harness/internal/tsig"
)

// CoordinatorFlags holds the command-line options that build a
// tsig.Coordinator, shared by every command that runs one.
type CoordinatorFlags struct {
	PublicPath string
	Signers    string
	Options    tsig.Options
	TracePath  string
}

// RegisterCoordinatorFlags defines the coordinator options on fs.
func RegisterCoordinatorFlags(fs *flag.FlagSet) *CoordinatorFlags {
	f := new(CoordinatorFlags)
	o := &f.Options
	fs.StringVar(&f.PublicPath, "threshold-public", "", "public.json written by qth-dealer (remote signer mode)")
	fs.StringVar(&f.Signers, "signers", "", "comma-separated signer addresses in ID order")
	fs.StringVar(&o.Policy, "policy", tsig.PolicyFixed, "fixed, fastest, first or hedge")
	fs.IntVar(&o.HedgeWidth, "hedge-width", 0, "signers engaged by the hedge policy; 0 means all")
	fs.Float64Var(&o.HedgeDelayFactor, "hedge-delay-factor", 0, "start with t signers and widen after this multiple of their expected response time; 0 widens at once")
	fs.Float64Var(&o.HedgeBudget, "hedge-budget", 0, "largest share of signatures that may widen after the delay; 0 means no limit")
	fs.Float64Var(&o.NudgeDelayFactor, "nudge-delay-factor", 0, "ping signers still silent after this multiple of the expected round trip; 0 disables")
	fs.StringVar(&o.Transport, "transport", tsig.TransportPersistent, "persistent, per-request, tls, tls-per-request, quic, quic-per-request or quic-0rtt")
	fs.IntVar(&o.Conns, "signer-conns", 1, "connections per signer of a persistent transport")
	fs.BoolVar(&o.Precompute, "precompute", false, "collect FROST commitments or ECDSA presignatures ahead of time")
	fs.StringVar(&o.SignerQlogDir, "signer-qlog", "", "existing directory for qlog files of the QUIC signer connections")
	fs.StringVar(&f.TracePath, "trace-out", "", "JSON lines file for per-signature traces")
	return f
}

// Coordinator connects to the signers and returns the coordinator. When a
// trace file is set, every signature made through crypto.Signer appends one
// JSON line to it.
func (f *CoordinatorFlags) Coordinator() (*tsig.Coordinator, error) {
	if f.PublicPath == "" {
		return nil, errors.New("coordd: -threshold-public is required")
	}
	pub, err := tsig.LoadPublic(f.PublicPath)
	if err != nil {
		return nil, err
	}
	opts := f.Options
	if f.TracePath != "" {
		if opts.OnTrace, err = traceWriter(f.TracePath); err != nil {
			return nil, err
		}
	}
	return tsig.NewCoordinator(pub, strings.Split(f.Signers, ","), opts)
}

// traceWriter returns a callback that appends traces to a new file at path.
// The file stays open for the life of the process.
func traceWriter(path string) (func(*tsig.Trace), error) {
	file, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	enc := json.NewEncoder(file)
	return func(trace *tsig.Trace) {
		mu.Lock()
		defer mu.Unlock()
		enc.Encode(trace)
	}, nil
}

// LoadLeaf reads the first certificate of a PEM file.
func LoadLeaf(path string) (*x509.Certificate, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("coordd: no certificate in %s", path)
	}
	return x509.ParseCertificate(block.Bytes)
}

// Certificate pairs leaf with a remote key. RSA and ECDSA keys are limited to
// the SHA-256 scheme, the only one coordinator and daemon implement.
func Certificate(leaf *x509.Certificate, key crypto.Signer) tls.Certificate {
	cert := tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: key, Leaf: leaf}
	switch key.Public().(type) {
	case *rsa.PublicKey:
		cert.SupportedSignatureAlgorithms = []tls.SignatureScheme{tls.PSSWithSHA256}
	case *ecdsa.PublicKey:
		cert.SupportedSignatureAlgorithms = []tls.SignatureScheme{tls.ECDSAWithP256AndSHA256}
	}
	return cert
}
