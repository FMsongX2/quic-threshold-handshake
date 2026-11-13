// Command qth-signbench drives a coordinator without QUIC and records where
// the time of each signature goes.
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"log"
	"os"
	"strings"
	"time"

	"github.com/FMsongX2/quic-threshold-handshake/harness/internal/tsig"
)

// A TLS 1.3 server CertificateVerify signs 64 spaces, a 33-byte context
// string, a zero byte and a SHA-256 transcript hash.
const ed25519MessageSize = 64 + 33 + 1 + 32

func main() {
	publicPath := flag.String("public", "", "public.json written by qth-dealer")
	signers := flag.String("signers", "", "comma-separated signer addresses in ID order")
	policy := flag.String("policy", tsig.PolicyFixed, "fixed, fastest, first or hedge")
	hedgeWidth := flag.Int("hedge-width", 0, "signers engaged by the hedge policy; 0 means all")
	hedgeDelay := flag.Float64("hedge-delay-factor", 0, "start with t signers and widen after this multiple of their expected response time; 0 widens at once")
	hedgeBudget := flag.Float64("hedge-budget", 0, "largest share of signatures that may widen after the delay; 0 means no limit")
	nudgeDelay := flag.Float64("nudge-delay-factor", 0, "ping signers still silent after this multiple of the expected round trip; 0 disables")
	transport := flag.String("transport", tsig.TransportPersistent, "persistent or per-request")
	conns := flag.Int("signer-conns", 1, "persistent connections per signer")
	precompute := flag.Bool("precompute", false, "collect FROST commitments ahead of time")
	n := flag.Int("n", 200, "number of measured signatures")
	warmup := flag.Int("warmup", 5, "signatures made before measuring")
	gap := flag.Duration("gap", 10*time.Millisecond, "pause between signatures")
	outPath := flag.String("out", "", "JSON lines output file")
	flag.Parse()

	pub, err := tsig.LoadPublic(*publicPath)
	if err != nil {
		log.Fatal(err)
	}
	coordinator, err := tsig.NewCoordinator(pub, strings.Split(*signers, ","), tsig.Options{
		Policy: *policy, HedgeWidth: *hedgeWidth, HedgeDelayFactor: *hedgeDelay, HedgeBudget: *hedgeBudget,
		NudgeDelayFactor: *nudgeDelay, Transport: *transport, Conns: *conns, Precompute: *precompute,
	})
	if err != nil {
		log.Fatal(err)
	}
	out, err := os.Create(*outPath)
	if err != nil {
		log.Fatal(err)
	}
	defer out.Close()
	enc := json.NewEncoder(out)

	size := 32
	if pub.Scheme == tsig.SchemeFrost || pub.Scheme == tsig.SchemePlainEd25519 {
		size = ed25519MessageSize
	}
	input := make([]byte, size)
	for i := range *warmup + *n {
		rand.Read(input)
		_, trace, err := coordinator.SignTraced(context.Background(), input)
		if err != nil {
			log.Fatalf("signature %d: %v", i, err)
		}
		if i >= *warmup {
			enc.Encode(trace)
		}
		time.Sleep(*gap)
	}
}
