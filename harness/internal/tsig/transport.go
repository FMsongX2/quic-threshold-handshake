package tsig

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/qlogwriter"
)

// Transport selects how the coordinator reaches a signer. The first of each
// pair keeps Options.Conns connections per signer; the others open a
// connection for every request.
const (
	// TransportPersistent multiplexes requests on plain TCP connections.
	TransportPersistent = "persistent"
	// TransportPerRequest opens a plain TCP connection for every request.
	TransportPerRequest = "per-request"
	// TransportTLS multiplexes requests on TLS 1.3 connections over TCP.
	TransportTLS = "tls"
	// TransportTLSPerRequest makes a full TLS 1.3 handshake over a new TCP
	// connection for every request.
	TransportTLSPerRequest = "tls-per-request"
	// TransportQUIC sends every request on its own stream of a QUIC
	// connection.
	TransportQUIC = "quic"
	// TransportQUICPerRequest makes a full QUIC handshake for every request.
	TransportQUICPerRequest = "quic-per-request"
	// TransportQUIC0RTT opens a QUIC connection for every request and, once
	// it holds a session ticket of the signer, sends the request as 0-RTT
	// data.
	TransportQUIC0RTT = "quic-0rtt"
)

type transportKind struct {
	quic, tls, perRequest, early bool
}

var transportKinds = map[string]transportKind{
	TransportPersistent:     {},
	TransportPerRequest:     {perRequest: true},
	TransportTLS:            {tls: true},
	TransportTLSPerRequest:  {tls: true, perRequest: true},
	TransportQUIC:           {quic: true},
	TransportQUICPerRequest: {quic: true, perRequest: true},
	TransportQUIC0RTT:       {quic: true, perRequest: true, early: true},
}

const (
	dialTimeout = 5 * time.Second
	// rttWindow is the length of one RTT estimation window.
	rttWindow = 5 * time.Second
	// signerALPN is the protocol name of the TLS and QUIC transports.
	signerALPN = "qth-sign"
)

// qlogFile flushes what a trace buffered before it closes the file.
type qlogFile struct {
	*bufio.Writer
	file *os.File
}

func (f qlogFile) Close() error {
	return errors.Join(f.Flush(), f.file.Close())
}

// link is one persistent connection to a signer.
type link interface {
	broken() bool
	fail(err error)
	roundTrip(ctx context.Context, op Op, payload []byte) (reply, error)
}

// peer is the coordinator's view of one signer.
type peer struct {
	id   int
	addr string
	kind transportKind
	// tlsConf is the client configuration of the TLS and QUIC transports.
	tlsConf *tls.Config
	// quicConf is the configuration of the QUIC transports.
	quicConf *quic.Config

	mu sync.Mutex
	// links are the persistent connections to the signer, dialed on first
	// use; next is the round-robin position.
	links []link
	next  int
	// endpoint is the UDP socket every QUIC connection to the signer uses.
	endpoint *quic.Transport
	// cur and prev are the smallest samples of round-trip time minus signer
	// compute time, in nanoseconds, in the current and the previous window.
	cur, prev float64
	curStart  time.Time
}

// newPeer describes signer id at addr. A non-empty qlogDir makes every QUIC
// connection to the signer write its qlog there.
func newPeer(id int, addr, transport string, conns int, qlogDir string) (*peer, error) {
	kind, ok := transportKinds[transport]
	if !ok {
		return nil, errors.New("tsig: unknown transport " + transport)
	}
	p := &peer{id: id, addr: addr, kind: kind, links: make([]link, conns)}
	if kind.quic {
		p.quicConf = &quic.Config{HandshakeIdleTimeout: dialTimeout, KeepAlivePeriod: 10 * time.Second}
		if qlogDir != "" {
			p.quicConf.Tracer = func(_ context.Context, isClient bool, connID quic.ConnectionID) qlogwriter.Trace {
				file, err := os.Create(filepath.Join(qlogDir, fmt.Sprintf("signer%d_%s.sqlog", id, connID)))
				if err != nil {
					return nil
				}
				trace := qlogwriter.NewConnectionFileSeq(qlogFile{bufio.NewWriter(file), file}, isClient, connID)
				go trace.Run()
				return trace
			}
		}
	}
	if kind.tls || kind.quic {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		p.tlsConf = &tls.Config{
			// A signer presents a self-signed certificate. The handshake
			// signature is still checked; the chain and the name are not.
			InsecureSkipVerify: true,
			ServerName:         host,
			NextProtos:         []string{signerALPN},
			MinVersion:         tls.VersionTLS13,
		}
		if kind.early {
			p.tlsConf.ClientSessionCache = tls.NewLRUClientSessionCache(1)
		}
	}
	return p, nil
}

// estimate is the smallest sample of the last two windows. A minimum ignores
// the samples a retransmission or a busy signer inflated; an average would
// let one such sample push a near signer behind a far one. Windows are
// measured in time, not in samples, so that a burst of concurrent requests
// delayed together cannot displace every normal sample.
func (p *peer) estimate() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return min(p.cur, p.prev)
}

func (p *peer) observe(sampleNs float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	switch {
	case p.curStart.IsZero():
		p.cur, p.prev, p.curStart = sampleNs, sampleNs, now
	case now.Sub(p.curStart) > rttWindow:
		p.prev, p.cur, p.curStart = p.cur, sampleNs, now
	default:
		p.cur = min(p.cur, sampleNs)
	}
}

// pick returns the persistent connection slot the next request should use.
func (p *peer) pick() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	slot := p.next % len(p.links)
	p.next++
	return slot
}

// call sends one request on the next connection in turn.
func (p *peer) call(ctx context.Context, op Op, payload []byte) (reply, time.Duration, error) {
	return p.callOn(ctx, p.pick(), op, payload)
}

// callOn sends one request on the persistent connection in slot and waits
// for its response; per-request transports ignore slot. The returned duration
// covers everything from just before the send to just after the receive,
// including the connection setup of a per-request transport.
func (p *peer) callOn(ctx context.Context, slot int, op Op, payload []byte) (reply, time.Duration, error) {
	start := time.Now()
	var r reply
	var err error
	switch {
	case !p.kind.perRequest:
		r, err = p.callPersistent(ctx, slot, op, payload)
	case p.kind.quic:
		r, err = p.callOnceQUIC(ctx, op, payload)
	default:
		r, err = p.callOnce(ctx, op, payload)
	}
	elapsed := time.Since(start)
	if err == nil {
		p.observe(float64(elapsed.Nanoseconds() - r.computeNs))
	}
	return r, elapsed, err
}

// record describes a finished request for the trace.
func (p *peer) record(r reply, elapsed time.Duration) Call {
	return Call{Signer: p.id, RTTNs: elapsed.Nanoseconds(), ComputeNs: r.computeNs, EarlyData: r.early}
}

// dialTCP opens a TCP connection and, for the TLS transports, completes the
// TLS handshake on it.
func (p *peer) dialTCP(ctx context.Context) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	if p.kind.tls {
		dialer := tls.Dialer{Config: p.tlsConf}
		return dialer.DialContext(ctx, "tcp", p.addr)
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp", p.addr)
}

// dialQUIC opens a QUIC connection. With 0-RTT and a session ticket it
// returns before the handshake completes.
func (p *peer) dialQUIC(ctx context.Context) (*quic.Conn, error) {
	remote, err := net.ResolveUDPAddr("udp", p.addr)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.endpoint == nil {
		network := "udp6"
		if remote.IP.To4() != nil {
			network = "udp4"
		}
		socket, err := net.ListenUDP(network, nil)
		if err != nil {
			p.mu.Unlock()
			return nil, err
		}
		p.endpoint = &quic.Transport{Conn: socket}
	}
	endpoint := p.endpoint
	p.mu.Unlock()
	if p.kind.early {
		return endpoint.DialEarly(ctx, remote, p.tlsConf, p.quicConf)
	}
	return endpoint.Dial(ctx, remote, p.tlsConf, p.quicConf)
}

func (p *peer) callOnce(ctx context.Context, op Op, payload []byte) (reply, error) {
	conn, err := p.dialTCP(ctx)
	if err != nil {
		return reply{}, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	if err := writeRequest(conn, op, 0, payload); err != nil {
		return reply{}, err
	}
	body, err := readFrame(conn)
	if err != nil {
		return reply{}, err
	}
	_, r, err := parseResponse(body)
	return r, err
}

func (p *peer) callOnceQUIC(ctx context.Context, op Op, payload []byte) (reply, error) {
	conn, err := p.dialQUIC(ctx)
	if err != nil {
		return reply{}, err
	}
	r, err := quicRoundTrip(ctx, conn, op, payload)
	if errors.Is(err, quic.Err0RTTRejected) {
		// The signer refused the early data; the handshake goes on and the
		// request is sent again once it completes.
		next, nextErr := conn.NextConnection(ctx)
		if nextErr != nil {
			conn.CloseWithError(0, "")
			return reply{}, nextErr
		}
		conn = next
		r, err = quicRoundTrip(ctx, conn, op, payload)
	}
	r.early = conn.ConnectionState().Used0RTT
	conn.CloseWithError(0, "")
	return r, err
}

func (p *peer) callPersistent(ctx context.Context, slot int, op Op, payload []byte) (reply, error) {
	p.mu.Lock()
	current := p.links[slot]
	p.mu.Unlock()
	if current == nil || current.broken() {
		// Dial without the lock so the slots of one signer open in parallel.
		fresh, err := p.dialLink(ctx)
		if err != nil {
			return reply{}, err
		}
		p.mu.Lock()
		if current = p.links[slot]; current != nil && !current.broken() {
			fresh.fail(errors.New("tsig: duplicate connection")) // another request opened the slot first
		} else {
			p.links[slot] = fresh
			current = fresh
		}
		p.mu.Unlock()
	}
	return current.roundTrip(ctx, op, payload)
}

func (p *peer) dialLink(ctx context.Context) (link, error) {
	if p.kind.quic {
		conn, err := p.dialQUIC(ctx)
		if err != nil {
			return nil, err
		}
		return quicLink{conn}, nil
	}
	conn, err := p.dialTCP(ctx)
	if err != nil {
		return nil, err
	}
	return newMuxConn(conn), nil
}

// quicLink carries every request on its own stream, so a lost packet delays
// only the requests it contained.
type quicLink struct {
	conn *quic.Conn
}

func (l quicLink) broken() bool { return l.conn.Context().Err() != nil }

func (l quicLink) fail(err error) { l.conn.CloseWithError(0, err.Error()) }

func (l quicLink) roundTrip(ctx context.Context, op Op, payload []byte) (reply, error) {
	return quicRoundTrip(ctx, l.conn, op, payload)
}

// quicRoundTrip sends one request on a new stream of conn and reads the
// response. Every frame carries its own length, so neither side waits for
// the end of the stream and a lost FIN delays nothing.
func quicRoundTrip(ctx context.Context, conn *quic.Conn, op Op, payload []byte) (reply, error) {
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return reply{}, err
	}
	// Ending the receive side here, without reading up to the signer's FIN,
	// gives the stream back to the signer's stream limit.
	defer stream.CancelRead(0)
	stop := context.AfterFunc(ctx, func() {
		stream.CancelWrite(0)
		stream.CancelRead(0)
	})
	defer stop()
	if err := writeRequest(stream, op, 0, payload); err != nil {
		return reply{}, err
	}
	// Closing right after the write lets the FIN travel with the request.
	stream.Close()
	body, err := readFrame(stream)
	if err != nil {
		if ctx.Err() != nil {
			err = errors.Join(errors.New("tsig: signer did not answer"), ctx.Err())
		}
		return reply{}, err
	}
	_, r, err := parseResponse(body)
	return r, err
}

type muxResult struct {
	reply reply
	err   error
}

// muxConn multiplexes concurrent requests over one TCP or TLS connection by
// request ID.
type muxConn struct {
	conn    net.Conn
	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[uint64]chan muxResult
	nextID  uint64
	err     error
}

func newMuxConn(conn net.Conn) *muxConn {
	m := &muxConn{conn: conn, pending: make(map[uint64]chan muxResult)}
	go m.readLoop()
	return m
}

func (m *muxConn) broken() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err != nil
}

func (m *muxConn) fail(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return
	}
	m.err = err
	m.conn.Close()
	for id, ch := range m.pending {
		ch <- muxResult{err: err}
		delete(m.pending, id)
	}
}

func (m *muxConn) readLoop() {
	for {
		body, err := readFrame(m.conn)
		if err != nil {
			m.fail(err)
			return
		}
		if len(body) < responseHeader {
			m.fail(errors.New("tsig: short response"))
			return
		}
		// A signer-side error still names the request it belongs to.
		id, r, err := parseResponse(body)
		m.mu.Lock()
		ch := m.pending[id]
		delete(m.pending, id)
		m.mu.Unlock()
		if ch != nil {
			ch <- muxResult{reply: r, err: err}
		}
	}
}

func (m *muxConn) roundTrip(ctx context.Context, op Op, payload []byte) (reply, error) {
	ch := make(chan muxResult, 1)
	m.mu.Lock()
	if m.err != nil {
		m.mu.Unlock()
		return reply{}, m.err
	}
	m.nextID++
	id := m.nextID
	m.pending[id] = ch
	m.mu.Unlock()

	m.writeMu.Lock()
	err := writeRequest(m.conn, op, id, payload)
	m.writeMu.Unlock()
	if err != nil {
		m.fail(err)
		return reply{}, err
	}

	select {
	case result := <-ch:
		return result.reply, result.err
	case <-ctx.Done():
		m.mu.Lock()
		delete(m.pending, id)
		m.mu.Unlock()
		return reply{}, errors.Join(errors.New("tsig: signer did not answer"), ctx.Err())
	}
}
