// Command qth-signer holds one key share and answers the coordinator.
package main

import (
	"flag"
	"log"
	"net"

	"github.com/FMsongX2/quic-threshold-handshake/harness/internal/tsig"
)

func main() {
	sharePath := flag.String("share", "", "share file written by qth-dealer")
	listen := flag.String("listen", ":7000", "TCP listen address")
	flag.Parse()

	share, err := tsig.LoadShare(*sharePath)
	if err != nil {
		log.Fatal(err)
	}
	signer, err := tsig.NewSigner(share)
	if err != nil {
		log.Fatal(err)
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("ready listen=%s scheme=%s id=%d", *listen, share.Scheme, share.ID)
	log.Fatal(signer.Serve(ln))
}
