package tsig

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"testing"
)

// ecdsaDeals caches ECDSA deals by threshold and signer count: each deal
// generates Paillier keys from safe primes, which takes tens of seconds.
var (
	ecdsaDealsMu sync.Mutex
	ecdsaDeals   = map[[2]int]dealt{}
)

type dealt struct {
	pub    *Public
	shares []*Share
}

func deal(t *testing.T, scheme string, threshold, signers int) (*Public, []*Share) {
	t.Helper()
	if scheme == SchemeECDSA {
		ecdsaDealsMu.Lock()
		defer ecdsaDealsMu.Unlock()
		if d, ok := ecdsaDeals[[2]int{threshold, signers}]; ok {
			return d.pub, d.shares
		}
	}
	pub, shares, _, err := Deal(scheme, threshold, signers)
	if err != nil {
		t.Fatal(err)
	}
	if scheme == SchemeECDSA {
		ecdsaDeals[[2]int{threshold, signers}] = dealt{pub, shares}
	}
	return pub, shares
}

// startSigners deals a key and runs one signer per share on loopback, over
// plain TCP.
func startSigners(t *testing.T, scheme string, threshold, signers int) (*Public, []string) {
	t.Helper()
	return startSignersOn(t, TransportPersistent, scheme, threshold, signers)
}

// startSignersOn is startSigners with the listener transport needs.
func startSignersOn(t *testing.T, transport, scheme string, threshold, signers int) (*Public, []string) {
	t.Helper()
	pub, shares := deal(t, scheme, threshold, signers)
	tlsConf, err := NewSignerTLS()
	if err != nil {
		t.Fatal(err)
	}
	kind := transportKinds[transport]
	var addrs []string
	for _, share := range shares {
		signer, err := NewSigner(share)
		if err != nil {
			t.Fatal(err)
		}
		if kind.quic {
			ln, err := ListenQUIC("127.0.0.1:0", tlsConf)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { ln.Close() })
			go signer.ServeQUIC(ln)
			addrs = append(addrs, ln.Addr().String())
			continue
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		if kind.tls {
			ln = tls.NewListener(ln, tlsConf)
		}
		t.Cleanup(func() { ln.Close() })
		go signer.Serve(ln)
		addrs = append(addrs, ln.Addr().String())
	}
	return pub, addrs
}

// A QUIC connection to a signer must outlive the request that opened it, and
// 0-RTT must carry the request itself once a ticket is held. A silent
// reconnect or a silent fall back to a full handshake would still produce
// valid signatures and change every measured delay.
func TestQUICTransportsReuseWhatTheyHold(t *testing.T) {
	message := []byte("message")

	pub, addrs := startSignersOn(t, TransportQUIC, SchemeFrost, 2, 3)
	coordinator, err := NewCoordinator(pub, addrs, Options{Transport: TransportQUIC})
	if err != nil {
		t.Fatal(err)
	}
	first := coordinator.peers[0].links[0]
	for range 3 {
		if _, _, err := coordinator.SignTraced(context.Background(), message); err != nil {
			t.Fatal(err)
		}
	}
	if coordinator.peers[0].links[0] != first || first.broken() {
		t.Fatal("the QUIC connection opened at startup was replaced")
	}

	pub, addrs = startSignersOn(t, TransportQUIC0RTT, SchemeFrost, 2, 3)
	if coordinator, err = NewCoordinator(pub, addrs, Options{Transport: TransportQUIC0RTT}); err != nil {
		t.Fatal(err)
	}
	_, trace, err := coordinator.SignTraced(context.Background(), message)
	if err != nil {
		t.Fatal(err)
	}
	for _, round := range trace.Rounds {
		for _, call := range round.Calls {
			if !call.EarlyData {
				t.Fatalf("round %s reached signer %d without 0-RTT", round.Name, call.Signer)
			}
		}
	}
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
		{SchemeFrost, 2, 5, Options{Policy: PolicyHedge}},
		{SchemeFrost, 3, 5, Options{Policy: PolicyHedge, HedgeWidth: 4, Precompute: true}},
		{SchemeFrost, 2, 3, Options{Policy: PolicyHedge, Transport: TransportPerRequest}},
		{SchemeFrost, 2, 5, Options{Policy: PolicyHedge, HedgeDelayFactor: 1.5}},
		{SchemeFrost, 3, 5, Options{Policy: PolicyHedge, HedgeWidth: 4, HedgeDelayFactor: 1.5, HedgeBudget: 0.2, Precompute: true}},
		{SchemeRSA, 2, 3, Options{}},
		{SchemeRSA, 2, 3, Options{NudgeDelayFactor: 0.001}},
		{SchemeRSA, 3, 5, Options{Policy: PolicyFirst, Conns: 4}},
		{SchemeFrost, 2, 3, Options{Policy: PolicyFastest, NudgeDelayFactor: 0.001, Conns: 4, Precompute: true}},
		{SchemeFrost, 2, 3, Options{Policy: PolicyFastest, NudgeDelayFactor: 0.001}},
		{SchemeRSA, 3, 5, Options{Policy: PolicyFirst}},
		{SchemeRSA, 2, 3, Options{Policy: PolicyFastest, Transport: TransportPerRequest}},
		{SchemeFrost, 2, 3, Options{Transport: TransportTLS, Conns: 2}},
		{SchemeRSA, 2, 3, Options{Transport: TransportTLSPerRequest}},
		{SchemeFrost, 3, 5, Options{Policy: PolicyFastest, Transport: TransportQUIC, NudgeDelayFactor: 0.001, Precompute: true}},
		{SchemeFrost, 2, 5, Options{Policy: PolicyHedge, Transport: TransportQUIC, Conns: 2}},
		{SchemeRSA, 3, 5, Options{Policy: PolicyFirst, Transport: TransportQUICPerRequest}},
		{SchemeFrost, 2, 3, Options{Transport: TransportQUIC0RTT}},
		{SchemeECDSA, 2, 3, Options{}},
		{SchemeECDSA, 2, 3, Options{Precompute: true}},
		{SchemeECDSA, 2, 3, Options{Transport: TransportQUIC}},
		{SchemeECDSA, 2, 3, Options{Policy: PolicyFastest, Transport: TransportQUIC, Precompute: true}},
		{SchemeECDSA, 3, 5, Options{Policy: PolicyFastest}},
		{SchemeECDSA, 3, 5, Options{Precompute: true}},
		{SchemeECDSA, 3, 5, Options{Transport: TransportQUIC}},
		{SchemeECDSA, 3, 5, Options{Transport: TransportQUIC, Precompute: true}},
	}
	for _, tc := range cases {
		name := tc.scheme + "/" + tc.opts.Policy + "/" + tc.opts.Transport
		if tc.opts.Precompute {
			name += "/precompute"
		}
		t.Run(name, func(t *testing.T) {
			pub, addrs := startSignersOn(t, tc.opts.Transport, tc.scheme, tc.threshold, tc.signers)
			coordinator, err := NewCoordinator(pub, addrs, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			message := []byte("TLS 1.3, server CertificateVerify")
			digest := sha256.Sum256(message)

			// More signatures than one precomputed batch holds, or than
			// half an ECDSA presignature pool, which starts a refill. An
			// ECDSA signature without a pool runs every round, so a few
			// of them suffice.
			count := commitBatchSize + 8
			switch {
			case tc.scheme == SchemeECDSA && tc.opts.Precompute:
				count = ecdsaPoolSize/2 + 2
			case tc.scheme == SchemeECDSA:
				count = 4
			}
			for range count {
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
				case *ecdsa.PublicKey:
					signature, err := coordinator.Sign(nil, digest[:], crypto.SHA256)
					if err != nil {
						t.Fatal(err)
					}
					if !ecdsa.VerifyASN1(public, digest[:], signature) {
						t.Fatal("ECDSA signature rejected")
					}
				}
			}
		})
	}
}

// ECDSA sessions keep per-session state on every signer, so signatures that
// overlap must not disturb each other.
func TestECDSAConcurrentSignatures(t *testing.T) {
	pub, addrs := startSigners(t, SchemeECDSA, 2, 3)
	coordinator, err := NewCoordinator(pub, addrs, Options{})
	if err != nil {
		t.Fatal(err)
	}
	public := coordinator.Public().(*ecdsa.PublicKey)
	const parallel = 24
	errs := make(chan error, parallel)
	for i := range parallel {
		go func() {
			digest := sha256.Sum256([]byte{byte(i)})
			signature, err := coordinator.Sign(nil, digest[:], crypto.SHA256)
			if err == nil && !ecdsa.VerifyASN1(public, digest[:], signature) {
				err = errors.New("ECDSA signature rejected")
			}
			errs <- err
		}()
	}
	for range parallel {
		if err := <-errs; err != nil {
			t.Error(err)
		}
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

// Hedged FROST runs a session per signer subset, so it finishes with the
// signers that are left when one is unreachable.
func TestFrostHedgeSurvivesMissingSigner(t *testing.T) {
	pub, addrs := startSigners(t, SchemeFrost, 2, 3)
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(pub, addrs, Options{Policy: PolicyHedge, Transport: TransportPerRequest})
	if err != nil {
		t.Fatal(err)
	}
	coordinator.peers[0].addr = dead.Addr().String()
	dead.Close()
	message := []byte("message")
	signature, trace, err := coordinator.SignTraced(context.Background(), message)
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(coordinator.Public().(ed25519.PublicKey), message, signature) {
		t.Fatal("Ed25519 signature rejected")
	}
	if trace.HedgeSessions != 1 {
		t.Fatalf("started %d sessions, want the one without the dead signer", trace.HedgeSessions)
	}
}

func TestCombinations(t *testing.T) {
	got := combinations(4, 2)
	want := [][]int{{0, 1}, {0, 2}, {0, 3}, {1, 2}, {1, 3}, {2, 3}}
	if len(got) != len(want) {
		t.Fatalf("got %d subsets, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i][0] != want[i][0] || got[i][1] != want[i][1] {
			t.Fatalf("subset %d is %v, want %v", i, got[i], want[i])
		}
	}
}

// A delayed hedge widens as soon as a primary signer fails, without waiting
// for the delay and without charging an exhausted budget.
func TestDelayedHedgeWidensOnSignerError(t *testing.T) {
	pub, addrs := startSigners(t, SchemeFrost, 2, 3)
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(pub, addrs, Options{
		Policy: PolicyHedge, Transport: TransportPerRequest, HedgeDelayFactor: 1e6, HedgeBudget: 0.01,
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator.hedgeTokens = 0
	// Make the nearest signer, which is always in the primary, unreachable.
	primary := coordinator.nearest(2)
	primary[0].addr = dead.Addr().String()
	dead.Close()
	message := []byte("message")
	signature, trace, err := coordinator.SignTraced(context.Background(), message)
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(coordinator.Public().(ed25519.PublicKey), message, signature) {
		t.Fatal("Ed25519 signature rejected")
	}
	if !trace.HedgeFired {
		t.Fatal("hedge did not widen after the primary signer failed")
	}
}
