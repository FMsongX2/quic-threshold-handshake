// Command qth-signer holds one key share and answers the coordinator.
package main

import (
	"crypto/tls"
	"flag"
	"log"
	"net"

	"github.com/FMsongX2/quic-threshold-handshake/harness/internal/tsig"
)

func main() {
	sharePath := flag.String("share", "", "share file written by qth-dealer")
	listen := flag.String("listen", ":7000", "TCP listen address")
	tlsListen := flag.String("tls-listen", ":7001", "TCP listen address of the TLS transport; empty disables it")
	quicListen := flag.String("quic-listen", ":7000", "UDP listen address of the QUIC transport; empty disables it")
	flag.Parse()

	share, err := tsig.LoadShare(*sharePath)
	if err != nil {
		log.Fatal(err)
	}
	signer, err := tsig.NewSigner(share)
	if err != nil {
		log.Fatal(err)
	}
	tlsConf, err := tsig.NewSignerTLS()
	if err != nil {
		log.Fatal(err)
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	if *tlsListen != "" {
		secure, err := tls.Listen("tcp", *tlsListen, tlsConf)
		if err != nil {
			log.Fatal(err)
		}
		go func() { log.Fatal(signer.Serve(secure)) }()
	}
	if *quicListen != "" {
		datagrams, err := tsig.ListenQUIC(*quicListen, tlsConf)
		if err != nil {
			log.Fatal(err)
		}
		go func() { log.Fatal(signer.ServeQUIC(datagrams)) }()
	}
	log.Printf("ready listen=%s tls=%s quic=%s scheme=%s id=%d", *listen, *tlsListen, *quicListen, share.Scheme, share.ID)
	log.Fatal(signer.Serve(ln))
}
