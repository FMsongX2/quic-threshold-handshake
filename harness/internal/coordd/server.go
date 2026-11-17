package coordd

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"fmt"
	"log"
	"net"
	"sync"
)

var pssOptions = &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: crypto.SHA256}

// SignMessage signs the unhashed TLS signing input with key in the one TLS
// 1.3 signature scheme the protocol assigns to the key type.
func SignMessage(key crypto.Signer, message []byte) ([]byte, error) {
	switch public := key.Public().(type) {
	case ed25519.PublicKey:
		return key.Sign(rand.Reader, message, crypto.Hash(0))
	case *rsa.PublicKey:
		digest := sha256.Sum256(message)
		return key.Sign(rand.Reader, digest[:], pssOptions)
	case *ecdsa.PublicKey:
		if public.Curve != elliptic.P256() {
			return nil, fmt.Errorf("coordd: ECDSA curve %s is not P-256", public.Curve.Params().Name)
		}
		digest := sha256.Sum256(message)
		return key.Sign(rand.Reader, digest[:], crypto.SHA256)
	default:
		return nil, fmt.Errorf("coordd: unsupported key type %T", public)
	}
}

// SignDigest signs the SHA-256 digest of the TLS signing input. A TLS stack
// whose asynchronous key hook only hands out the digest uses this form; an
// Ed25519 key cannot serve it, because Ed25519 signs the message itself.
func SignDigest(key crypto.Signer, digest []byte) ([]byte, error) {
	if len(digest) != sha256.Size {
		return nil, fmt.Errorf("coordd: digest of %d bytes is not SHA-256", len(digest))
	}
	switch public := key.Public().(type) {
	case *rsa.PublicKey:
		return key.Sign(rand.Reader, digest, pssOptions)
	case *ecdsa.PublicKey:
		if public.Curve != elliptic.P256() {
			return nil, fmt.Errorf("coordd: ECDSA curve %s is not P-256", public.Curve.Params().Name)
		}
		return key.Sign(rand.Reader, digest, crypto.SHA256)
	default:
		return nil, fmt.Errorf("coordd: key type %T cannot sign a digest", public)
	}
}

// Serve answers requests that carry the unhashed signing input on ln with
// key until ln fails.
func Serve(ln net.Listener, key crypto.Signer) error {
	return serve(ln, key, SignMessage)
}

// ServeDigests answers requests that carry a SHA-256 digest on ln with key
// until ln fails.
func ServeDigests(ln net.Listener, key crypto.Signer) error {
	return serve(ln, key, SignDigest)
}

func serve(ln net.Listener, key crypto.Signer, sign func(crypto.Signer, []byte) ([]byte, error)) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go serveConn(conn, key, sign)
	}
}

// serveConn signs every request in its own goroutine, so one slow signature
// does not hold back the others on the same connection.
func serveConn(conn net.Conn, key crypto.Signer, sign func(crypto.Signer, []byte) ([]byte, error)) {
	defer conn.Close()
	var writeMu sync.Mutex
	for {
		body, err := readFrame(conn)
		if err != nil {
			return
		}
		id, input, err := parseRequest(body)
		if err != nil {
			log.Printf("coordd: %s: %v", conn.RemoteAddr(), err)
			return
		}
		go func() {
			signature, failure := sign(key, input)
			if failure != nil {
				log.Printf("coordd: request %d: %v", id, failure)
			}
			writeMu.Lock()
			defer writeMu.Unlock()
			if err := writeResponse(conn, id, signature, failure); err != nil {
				conn.Close()
			}
		}()
	}
}
