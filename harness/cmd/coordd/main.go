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
	listen := flag.String("listen", coordd.DefaultAddr, "TCP listen address")
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
	log.Printf("ready listen=%s signers=%s policy=%s transport=%s",
		ln.Addr(), coordFlags.Signers, coordFlags.Options.Policy, coordFlags.Options.Transport)
	log.Fatal(coordd.Serve(ln, coordinator))
}
