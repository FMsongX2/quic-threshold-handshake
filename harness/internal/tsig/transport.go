package tsig

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

// Transport selects how the coordinator reaches a signer.
const (
	// TransportPersistent keeps one TCP connection per signer and multiplexes
	// requests on it.
	TransportPersistent = "persistent"
	// TransportPerRequest opens a new TCP connection for every request.
	TransportPerRequest = "per-request"
)

const dialTimeout = 5 * time.Second

// peer is the coordinator's view of one signer.
type peer struct {
	id        int
	addr      string
	transport string

	mu   sync.Mutex
	conn *muxConn
	// rttNs is a moving average of round-trip time minus signer compute time.
	rttNs float64
}

func (p *peer) estimate() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rttNs
}

func (p *peer) observe(sampleNs float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rttNs == 0 {
		p.rttNs = sampleNs
	} else {
		p.rttNs = 0.8*p.rttNs + 0.2*sampleNs
	}
}

// call sends one request and waits for its response. The returned duration
// covers everything from just before the send to just after the receive,
// including the TCP handshake in per-request mode.
func (p *peer) call(ctx context.Context, op Op, payload []byte) (reply, time.Duration, error) {
	start := time.Now()
	var r reply
	var err error
	if p.transport == TransportPerRequest {
		r, err = p.callOnce(ctx, op, payload)
	} else {
		r, err = p.callPersistent(ctx, op, payload)
	}
	elapsed := time.Since(start)
	if err == nil {
		p.observe(float64(elapsed.Nanoseconds() - r.computeNs))
	}
	return r, elapsed, err
}

func (p *peer) callOnce(ctx context.Context, op Op, payload []byte) (reply, error) {
	dialer := net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", p.addr)
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

func (p *peer) callPersistent(ctx context.Context, op Op, payload []byte) (reply, error) {
	p.mu.Lock()
	conn := p.conn
	if conn == nil || conn.broken() {
		raw, err := net.DialTimeout("tcp", p.addr, dialTimeout)
		if err != nil {
			p.mu.Unlock()
			return reply{}, err
		}
		conn = newMuxConn(raw)
		p.conn = conn
	}
	p.mu.Unlock()
	return conn.roundTrip(ctx, op, payload)
}

type muxResult struct {
	reply reply
	err   error
}

// muxConn multiplexes concurrent requests over one connection by request ID.
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
