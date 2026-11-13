// Command qth-client opens QUIC connections in waves and records how long
// each full handshake takes.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"log"
	"os"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/qlog"
	"github.com/quic-go/quic-go/qlogwriter"
)

type record struct {
	Index       int     `json:"i"`
	Wave        int     `json:"wave"`
	Warmup      bool    `json:"warmup"`
	StartUnixNs int64   `json:"start_unix_ns"`
	THsMs       float64 `json:"t_hs_ms"`
	OK          bool    `json:"ok"`
	Err         string  `json:"err,omitempty"`
	ODCID       string  `json:"odcid"`
}

func main() {
	addr := flag.String("addr", "", "server address (host:port)")
	sni := flag.String("sni", "server", "TLS server name")
	caPath := flag.String("ca", "", "PEM file with the trusted server certificate")
	alpn := flag.String("alpn", "qth", "ALPN protocol")
	n := flag.Int("n", 50, "number of measured handshakes")
	conc := flag.Int("c", 1, "handshakes started at the same instant per wave")
	warmup := flag.Int("warmup", 1, "handshakes run before measuring, excluded from statistics")
	timeout := flag.Duration("timeout", 30*time.Second, "per-handshake timeout")
	gap := flag.Duration("gap", 100*time.Millisecond, "pause between waves")
	outPath := flag.String("out", "", "JSON lines output file")
	flag.Parse()
	if *addr == "" || *caPath == "" || *outPath == "" {
		log.Fatal("-addr, -ca and -out are required")
	}

	pem, err := os.ReadFile(*caPath)
	if err != nil {
		log.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		log.Fatalf("no certificate in %s", *caPath)
	}
	// No ClientSessionCache: every connection performs a full handshake.
	tlsConf := &tls.Config{
		RootCAs:    pool,
		ServerName: *sni,
		NextProtos: []string{*alpn},
		MinVersion: tls.VersionTLS13,
	}

	out, err := os.Create(*outPath)
	if err != nil {
		log.Fatal(err)
	}
	defer out.Close()
	enc := json.NewEncoder(out)

	dial := func(rec *record) {
		conf := &quic.Config{
			HandshakeIdleTimeout: *timeout,
			Tracer: func(ctx context.Context, isClient bool, id quic.ConnectionID) qlogwriter.Trace {
				rec.ODCID = id.String()
				return qlog.DefaultConnectionTracer(ctx, isClient, id)
			},
		}
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		defer cancel()
		start := time.Now()
		conn, err := quic.DialAddr(ctx, *addr, tlsConf, conf)
		elapsed := time.Since(start)
		rec.StartUnixNs = start.UnixNano()
		rec.THsMs = float64(elapsed.Nanoseconds()) / 1e6
		if err != nil {
			rec.Err = err.Error()
			return
		}
		rec.OK = true
		conn.CloseWithError(0, "")
	}

	index := 0
	for range *warmup {
		rec := record{Index: index, Wave: -1, Warmup: true}
		dial(&rec)
		enc.Encode(rec)
		index++
		time.Sleep(*gap)
	}

	for wave := 0; index-*warmup < *n; wave++ {
		size := min(*conc, *n-(index-*warmup))
		recs := make([]record, size)
		var ready, done sync.WaitGroup
		startGate := make(chan struct{})
		for k := range recs {
			recs[k] = record{Index: index + k, Wave: wave}
			ready.Add(1)
			done.Add(1)
			go func(rec *record) {
				defer done.Done()
				ready.Done()
				<-startGate
				dial(rec)
			}(&recs[k])
		}
		ready.Wait()
		close(startGate)
		done.Wait()
		for k := range recs {
			enc.Encode(recs[k])
		}
		index += size
		time.Sleep(*gap)
	}

	// Let closed connections flush their qlog files.
	time.Sleep(500 * time.Millisecond)
}
