package tsig

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"net"
	"sync"
	"time"

	"github.com/bytemare/ecc"
	"github.com/bytemare/frost"
	"github.com/bytemare/secret-sharing/keys"
	trsa "github.com/cloudflare/circl/tss/rsa"
	"github.com/quic-go/quic-go"
)

// maxSignerStreams is the number of requests a coordinator may have in
// flight on one QUIC connection.
const maxSignerStreams = 1 << 16

// pssOptions is the padding TLS 1.3 uses for rsa_pss_rsae_sha256.
var pssOptions = &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: crypto.SHA256}

// frostMu serializes every call into the frost library. The library hashes
// with one shared hasher per ciphersuite and keeps signer nonces in plain
// maps, so concurrent calls in one process corrupt its state.
var frostMu sync.Mutex

// Signer answers signing requests with one key share.
type Signer struct {
	handle func(op Op, payload []byte) ([]byte, error)
}

// NewSigner builds the request handler for the scheme of share.
func NewSigner(share *Share) (*Signer, error) {
	switch share.Scheme {
	case SchemePlainEd25519, SchemePlainRSA:
		key, err := x509.ParsePKCS8PrivateKey(share.Secret)
		if err != nil {
			return nil, err
		}
		return &Signer{handle: func(op Op, payload []byte) ([]byte, error) {
			if op != OpPlainSign {
				return nil, errUnsupportedOp(op)
			}
			switch key := key.(type) {
			case ed25519.PrivateKey:
				return ed25519.Sign(key, payload), nil
			case *rsa.PrivateKey:
				return rsa.SignPSS(rand.Reader, key, crypto.SHA256, payload, pssOptions)
			}
			return nil, errors.New("tsig: unsupported plain key type")
		}}, nil

	case SchemeRSA:
		public, err := share.publicKey()
		if err != nil {
			return nil, err
		}
		rsaPublic, ok := public.(*rsa.PublicKey)
		if !ok {
			return nil, errors.New("tsig: rsa share without RSA public key")
		}
		keyShare := new(trsa.KeyShare)
		if err := keyShare.UnmarshalBinary(share.Secret); err != nil {
			return nil, err
		}
		return &Signer{handle: func(op Op, payload []byte) ([]byte, error) {
			if op != OpRSAShare {
				return nil, errUnsupportedOp(op)
			}
			signShare, err := keyShare.Sign(rand.Reader, rsaPublic, payload, true)
			if err != nil {
				return nil, err
			}
			return signShare.MarshalBinary()
		}}, nil

	case SchemeFrost:
		conf, err := share.frostConfig()
		if err != nil {
			return nil, err
		}
		keyShare := keys.NewKeyShareReceiver(ecc.Edwards25519Sha512)
		if err := keyShare.Decode(share.Secret); err != nil {
			return nil, err
		}
		signer, err := conf.Signer(keyShare)
		if err != nil {
			return nil, err
		}
		return &Signer{handle: func(op Op, payload []byte) ([]byte, error) {
			frostMu.Lock()
			defer frostMu.Unlock()
			switch op {
			case OpFrostCommit:
				if len(payload) != 2 {
					return nil, errors.New("tsig: malformed commit request")
				}
				count := binary.BigEndian.Uint16(payload)
				if count == 0 || count > maxCommitBatch {
					return nil, errors.New("tsig: commit batch size out of range")
				}
				var out []byte
				for range count {
					out = append(out, signer.Commit().Encode()...)
				}
				return out, nil
			case OpFrostSign:
				message, lists, err := decodeFrostSign(payload)
				if err != nil {
					return nil, err
				}
				var out []byte
				for _, list := range lists {
					sigShare, err := signer.Sign(message, list)
					if err != nil {
						return nil, err
					}
					out = append(out, sigShare.Encode()...)
				}
				return out, nil
			}
			return nil, errUnsupportedOp(op)
		}}, nil

	case SchemeECDSA:
		return newECDSASigner(share)
	}
	return nil, fmt.Errorf("tsig: unknown scheme %q", share.Scheme)
}

func errUnsupportedOp(op Op) error {
	return fmt.Errorf("tsig: operation %d not supported by this signer", op)
}

// encodeFrostSign builds a sign request: one message and the commitment list
// of every session the signer takes part in. The response carries one
// signature share per list, in order.
func encodeFrostSign(message []byte, lists []frost.CommitmentList) []byte {
	out := binary.BigEndian.AppendUint16(nil, uint16(len(message)))
	out = append(out, message...)
	out = binary.BigEndian.AppendUint16(out, uint16(len(lists)))
	for _, list := range lists {
		encoded := list.Encode()
		out = binary.BigEndian.AppendUint16(out, uint16(len(encoded)))
		out = append(out, encoded...)
	}
	return out
}

func decodeFrostSign(payload []byte) ([]byte, []frost.CommitmentList, error) {
	malformed := errors.New("tsig: malformed sign request")
	next := func() ([]byte, bool) {
		if len(payload) < 2 {
			return nil, false
		}
		size := int(binary.BigEndian.Uint16(payload))
		if len(payload) < 2+size {
			return nil, false
		}
		field := payload[2 : 2+size]
		payload = payload[2+size:]
		return field, true
	}
	message, ok := next()
	if !ok || len(payload) < 2 {
		return nil, nil, malformed
	}
	count := int(binary.BigEndian.Uint16(payload))
	payload = payload[2:]
	if count == 0 || count > maxCommitBatch {
		return nil, nil, malformed
	}
	lists := make([]frost.CommitmentList, count)
	for i := range lists {
		encoded, ok := next()
		if !ok {
			return nil, nil, malformed
		}
		list, err := frost.DecodeList(encoded)
		if err != nil {
			return nil, nil, err
		}
		lists[i] = list
	}
	return message, lists, nil
}

// NewSignerTLS returns the server configuration of the TLS and QUIC
// transports, with a fresh self-signed Ed25519 certificate.
func NewSignerTLS() (*tls.Config, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: private}},
		NextProtos:   []string{signerALPN},
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// ListenQUIC listens for coordinators on a UDP address. It accepts 0-RTT
// data, and lets a coordinator keep maxSignerStreams requests in flight on
// one connection.
func ListenQUIC(addr string, conf *tls.Config) (*quic.EarlyListener, error) {
	return quic.ListenAddrEarly(addr, conf, &quic.Config{Allow0RTT: true, MaxIncomingStreams: maxSignerStreams})
}

// answer handles one request and reports the time it took.
func (s *Signer) answer(op Op, payload []byte) (out []byte, computeNs int64, failure error) {
	start := time.Now()
	if op != OpPing {
		out, failure = s.handle(op, payload)
	}
	return out, time.Since(start).Nanoseconds(), failure
}

// Serve accepts coordinator connections on ln, which is a TCP or a TLS
// listener, until ln is closed. Requests on one connection are handled
// concurrently and answered in completion order.
func (s *Signer) Serve(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go s.serveConn(conn)
	}
}

func (s *Signer) serveConn(conn net.Conn) {
	defer conn.Close()
	var writeMu sync.Mutex
	for {
		body, err := readFrame(conn)
		if err != nil {
			return
		}
		op, id, payload, err := parseRequest(body)
		if err != nil {
			return
		}
		go func() {
			out, computeNs, failure := s.answer(op, payload)
			writeMu.Lock()
			defer writeMu.Unlock()
			writeResponse(conn, id, computeNs, out, failure)
		}()
	}
}

// ServeQUIC accepts coordinator connections on ln until ln is closed. Every
// stream carries one request and its response. Connections are accepted
// before their handshake completes, so a request sent as 0-RTT data is
// answered without waiting for it.
func (s *Signer) ServeQUIC(ln *quic.EarlyListener) error {
	for {
		conn, err := ln.Accept(context.Background())
		if err != nil {
			return err
		}
		go func() {
			for {
				stream, err := conn.AcceptStream(context.Background())
				if err != nil {
					return
				}
				go s.serveStream(stream)
			}
		}()
	}
}

func (s *Signer) serveStream(stream *quic.Stream) {
	// The request frame is all this side reads; ending the receive side
	// without waiting for the coordinator's FIN frees the stream.
	defer stream.CancelRead(0)
	defer stream.Close()
	body, err := readFrame(stream)
	if err != nil {
		return
	}
	op, id, payload, err := parseRequest(body)
	if err != nil {
		return
	}
	out, computeNs, failure := s.answer(op, payload)
	writeResponse(stream, id, computeNs, out, failure)
}
