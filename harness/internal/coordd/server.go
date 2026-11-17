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

// Serve answers signing requests on ln with key until ln fails. Every request
// is signed in its own goroutine, so one slow signature does not hold back
// the others on the same connection.
func Serve(ln net.Listener, key crypto.Signer) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go serveConn(conn, key)
	}
}

func serveConn(conn net.Conn, key crypto.Signer) {
	defer conn.Close()
	var writeMu sync.Mutex
	for {
		body, err := readFrame(conn)
		if err != nil {
			return
		}
		id, message, err := parseRequest(body)
		if err != nil {
			log.Printf("coordd: %s: %v", conn.RemoteAddr(), err)
			return
		}
		go func() {
			signature, failure := SignMessage(key, message)
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
