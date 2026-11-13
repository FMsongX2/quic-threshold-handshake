// Command qth-dealer generates a key, splits it into shares and writes the
// certificate, the public material and one share file per signer.
package main

import (
	"flag"
	"log"

	"github.com/FMsongX2/quic-threshold-handshake/harness/internal/tsig"
)

func main() {
	scheme := flag.String("scheme", tsig.SchemeFrost, "frost, rsa, plain-ed25519 or plain-rsa")
	threshold := flag.Int("t", 2, "shares needed for a signature")
	signers := flag.Int("n", 3, "number of signers")
	out := flag.String("out", "", "output directory")
	flag.Parse()
	if *out == "" {
		log.Fatal("-out is required")
	}

	pub, shares, cert, err := tsig.Deal(*scheme, *threshold, *signers)
	if err != nil {
		log.Fatal(err)
	}
	if err := tsig.WriteDeal(*out, pub, shares, cert); err != nil {
		log.Fatal(err)
	}
	log.Printf("dealt scheme=%s t=%d n=%d dir=%s", *scheme, *threshold, *signers, *out)
}
