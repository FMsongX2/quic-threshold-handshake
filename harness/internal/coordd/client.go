package coordd

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// requestTimeout bounds one signature; it exceeds the coordinator's own
// signing timeout so the daemon reports its errors first.
const requestTimeout = 30 * time.Second

type result struct {
	signature []byte
	err       error
}

// Client is a certificate key whose signatures come from a daemon. It keeps
// one connection and multiplexes concurrent signatures over it; a broken
// connection is replaced on the next signature.
type Client struct {
	addr   string
	public crypto.PublicKey

	mu   sync.Mutex
	conn *clientConn
}

type clientConn struct {
	conn    net.Conn
	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[uint64]chan result
	nextID  uint64
	err     error
}

// NewClient returns a key for the daemon at addr whose public half is public,
// normally the key in the certificate.
func NewClient(addr string, public crypto.PublicKey) *Client {
	return &Client{addr: addr, public: public}
}

// Public returns the key in the certificate.
func (c *Client) Public() crypto.PublicKey { return c.public }

// Sign rejects pre-hashed input: the daemon needs the unhashed message, which
// crypto/tls passes to SignMessage.
func (c *Client) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	return nil, errors.New("coordd: the daemon signs only unhashed messages")
}

// SignMessage implements crypto.MessageSigner. opts must name the scheme the
// daemon uses for this key type, so a mismatch fails here instead of
// producing a signature the peer rejects.
func (c *Client) SignMessage(_ io.Reader, message []byte, opts crypto.SignerOpts) ([]byte, error) {
	switch c.public.(type) {
	case ed25519.PublicKey:
		if opts.HashFunc() != crypto.Hash(0) {
			return nil, errors.New("coordd: Ed25519 signs the unhashed message")
		}
	case *rsa.PublicKey:
		if pss, ok := opts.(*rsa.PSSOptions); !ok || pss.Hash != crypto.SHA256 {
			return nil, errors.New("coordd: RSA supports only PSS with SHA-256")
		}
	case *ecdsa.PublicKey:
		if opts.HashFunc() != crypto.SHA256 {
			return nil, errors.New("coordd: ECDSA supports only SHA-256")
		}
	default:
		return nil, fmt.Errorf("coordd: unsupported key type %T", c.public)
	}
	conn, err := c.connection()
	if err != nil {
		return nil, err
	}
	return conn.roundTrip(message)
}

func (c *Client) connection() (*clientConn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil && !c.conn.broken() {
		return c.conn, nil
	}
	conn, err := net.DialTimeout("tcp", c.addr, requestTimeout)
	if err != nil {
		return nil, fmt.Errorf("coordd: %w", err)
	}
	c.conn = &clientConn{conn: conn, pending: make(map[uint64]chan result)}
	go c.conn.readLoop()
	return c.conn, nil
}

func (m *clientConn) broken() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err != nil
}

func (m *clientConn) fail(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return
	}
	m.err = err
	m.conn.Close()
	for id, ch := range m.pending {
		ch <- result{err: err}
		delete(m.pending, id)
	}
}

func (m *clientConn) readLoop() {
	for {
		body, err := readFrame(m.conn)
		if err != nil {
			m.fail(fmt.Errorf("coordd: %w", err))
			return
		}
		if len(body) < responseHeader {
			m.fail(errors.New("coordd: short response"))
			return
		}
		// A daemon-side error still names the request it belongs to.
		id, signature, err := parseResponse(body)
		m.mu.Lock()
		ch := m.pending[id]
		delete(m.pending, id)
		m.mu.Unlock()
		if ch != nil {
			ch <- result{signature: signature, err: err}
		}
	}
}

func (m *clientConn) roundTrip(message []byte) ([]byte, error) {
	ch := make(chan result, 1)
	m.mu.Lock()
	if m.err != nil {
		m.mu.Unlock()
		return nil, m.err
	}
	m.nextID++
	id := m.nextID
	m.pending[id] = ch
	m.mu.Unlock()

	m.writeMu.Lock()
	err := writeRequest(m.conn, id, message)
	m.writeMu.Unlock()
	if err != nil {
		m.fail(fmt.Errorf("coordd: %w", err))
		return nil, err
	}

	timer := time.NewTimer(requestTimeout)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.signature, r.err
	case <-timer.C:
		m.mu.Lock()
		delete(m.pending, id)
		m.mu.Unlock()
		return nil, errors.New("coordd: daemon did not answer")
	}
}
