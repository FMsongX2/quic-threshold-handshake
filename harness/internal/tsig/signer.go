package tsig

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/bytemare/ecc"
	"github.com/bytemare/frost"
	"github.com/bytemare/secret-sharing/keys"
	trsa "github.com/cloudflare/circl/tss/rsa"
)

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
				message, list, err := decodeFrostSign(payload)
				if err != nil {
					return nil, err
				}
				sigShare, err := signer.Sign(message, list)
				if err != nil {
					return nil, err
				}
				return sigShare.Encode(), nil
			}
			return nil, errUnsupportedOp(op)
		}}, nil
	}
	return nil, fmt.Errorf("tsig: unknown scheme %q", share.Scheme)
}

func errUnsupportedOp(op Op) error {
	return fmt.Errorf("tsig: operation %d not supported by this signer", op)
}

func encodeFrostSign(message []byte, list frost.CommitmentList) []byte {
	out := binary.BigEndian.AppendUint16(nil, uint16(len(message)))
	out = append(out, message...)
	return append(out, list.Encode()...)
}

func decodeFrostSign(payload []byte) ([]byte, frost.CommitmentList, error) {
	if len(payload) < 2 {
		return nil, nil, errors.New("tsig: malformed sign request")
	}
	size := int(binary.BigEndian.Uint16(payload))
	if len(payload) < 2+size {
		return nil, nil, errors.New("tsig: malformed sign request")
	}
	list, err := frost.DecodeList(payload[2+size:])
	return payload[2 : 2+size], list, err
}

// Serve accepts coordinator connections on ln until ln is closed. Requests on
// one connection are handled concurrently and answered in completion order.
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
			start := time.Now()
			var out []byte
			var failure error
			if op != OpPing {
				out, failure = s.handle(op, payload)
			}
			elapsed := time.Since(start).Nanoseconds()
			writeMu.Lock()
			defer writeMu.Unlock()
			writeResponse(conn, id, elapsed, out, failure)
		}()
	}
}
