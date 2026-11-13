package tsig

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"net"
	"testing"
)

// startSigners deals a key and runs one signer per share on loopback.
func startSigners(t *testing.T, scheme string, threshold, signers int) (*Public, []string) {
	t.Helper()
	pub, shares, _, err := Deal(scheme, threshold, signers)
	if err != nil {
		t.Fatal(err)
	}
	var addrs []string
	for _, share := range shares {
		signer, err := NewSigner(share)
		if err != nil {
			t.Fatal(err)
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		go signer.Serve(ln)
		addrs = append(addrs, ln.Addr().String())
	}
	return pub, addrs
}

// The coordinator's output must be accepted by the standard library verifier
// a TLS client uses, for every scheme, policy and transport.
func TestSignaturesVerifyWithStandardLibrary(t *testing.T) {
	cases := []struct {
		scheme             string
		threshold, signers int
		opts               Options
	}{
		{SchemePlainEd25519, 1, 1, Options{}},
		{SchemePlainRSA, 1, 1, Options{}},
		{SchemeFrost, 2, 3, Options{}},
		{SchemeFrost, 3, 5, Options{Policy: PolicyFastest}},
		{SchemeFrost, 2, 3, Options{Precompute: true}},
		{SchemeFrost, 2, 3, Options{Transport: TransportPerRequest}},
		{SchemeRSA, 2, 3, Options{}},
		{SchemeRSA, 3, 5, Options{Policy: PolicyFirst}},
		{SchemeRSA, 2, 3, Options{Policy: PolicyFastest, Transport: TransportPerRequest}},
	}
	for _, tc := range cases {
		name := tc.scheme + "/" + tc.opts.Policy + "/" + tc.opts.Transport
		t.Run(name, func(t *testing.T) {
			pub, addrs := startSigners(t, tc.scheme, tc.threshold, tc.signers)
			coordinator, err := NewCoordinator(pub, addrs, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			message := []byte("TLS 1.3, server CertificateVerify")
			digest := sha256.Sum256(message)

			// More signatures than one precomputed batch holds.
			for range commitBatchSize + 8 {
				switch public := coordinator.Public().(type) {
				case ed25519.PublicKey:
					signature, err := coordinator.Sign(nil, message, crypto.Hash(0))
					if err != nil {
						t.Fatal(err)
					}
					if !ed25519.Verify(public, message, signature) {
						t.Fatal("Ed25519 signature rejected")
					}
				case *rsa.PublicKey:
					signature, err := coordinator.Sign(nil, digest[:], pssOptions)
					if err != nil {
						t.Fatal(err)
					}
					if err := rsa.VerifyPSS(public, crypto.SHA256, digest[:], signature, pssOptions); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}

// A FROST signature needs every chosen signer, so losing one of the t fixed
// signers must fail instead of returning a bad signature.
func TestFrostFailsWhenChosenSignerIsDown(t *testing.T) {
	pub, addrs := startSigners(t, SchemeFrost, 2, 3)
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(pub, addrs, Options{Transport: TransportPerRequest})
	if err != nil {
		t.Fatal(err)
	}
	coordinator.peers[1].addr = dead.Addr().String()
	dead.Close()
	if _, _, err := coordinator.SignTraced(context.Background(), []byte("message")); err == nil {
		t.Fatal("signature succeeded without a chosen signer")
	}
}

// Threshold RSA with the first policy tolerates n - t unreachable signers.
func TestRSAFirstPolicySurvivesMissingSigners(t *testing.T) {
	pub, addrs := startSigners(t, SchemeRSA, 2, 3)
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(pub, addrs, Options{Policy: PolicyFirst, Transport: TransportPerRequest})
	if err != nil {
		t.Fatal(err)
	}
	coordinator.peers[0].addr = dead.Addr().String()
	dead.Close()
	digest := sha256.Sum256([]byte("message"))
	if _, _, err := coordinator.SignTraced(context.Background(), digest[:]); err != nil {
		t.Fatal(err)
	}
}
