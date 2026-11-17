// Command qth-coordd runs a tsig.Coordinator as a local signing daemon, so a
// QUIC server in any language can use it as its certificate key. The wire
// protocol is described in package coordd.
package main

import (
	"flag"
	"log"
	"net"

	"github.com/FMsongX2/quic-threshold-handshake/harness/internal/coordd"
)

func main() {
	listen := flag.String("listen", coordd.DefaultAddr, "TCP listen address for requests that carry the signing input")
	digestListen := flag.String("digest-listen", "", "TCP listen address for requests that carry its SHA-256 digest; empty disables it")
	coordFlags := coordd.RegisterCoordinatorFlags(flag.CommandLine)
	flag.Parse()

	coordinator, err := coordFlags.Coordinator()
	if err != nil {
		log.Fatal(err)
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	if *digestListen != "" {
		digests, err := net.Listen("tcp", *digestListen)
		if err != nil {
			log.Fatal(err)
		}
		go func() { log.Fatal(coordd.ServeDigests(digests, coordinator)) }()
	}
	log.Printf("ready listen=%s digest_listen=%s signers=%s policy=%s transport=%s",
		ln.Addr(), *digestListen, coordFlags.Signers, coordFlags.Options.Policy, coordFlags.Options.Transport)
	log.Fatal(coordd.Serve(ln, coordinator))
}
