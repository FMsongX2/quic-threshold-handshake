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
	"net"
	"sync"
	"testing"
)

func TestClientServer(t *testing.T) {
	_, edKey, _ := ed25519.GenerateKey(rand.Reader)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		key    crypto.Signer
		opts   crypto.SignerOpts
		verify func(message, signature []byte) bool
	}{
		{"ed25519", edKey, crypto.Hash(0), func(m, s []byte) bool {
			return ed25519.Verify(edKey.Public().(ed25519.PublicKey), m, s)
		}},
		{"rsa", rsaKey, pssOptions, func(m, s []byte) bool {
			digest := sha256.Sum256(m)
			return rsa.VerifyPSS(&rsaKey.PublicKey, crypto.SHA256, digest[:], s, pssOptions) == nil
		}},
		{"ecdsa", ecKey, crypto.SHA256, func(m, s []byte) bool {
			digest := sha256.Sum256(m)
			return ecdsa.VerifyASN1(&ecKey.PublicKey, digest[:], s)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			go Serve(ln, tc.key)

			client := NewClient(ln.Addr().String(), tc.key.Public())
			var wg sync.WaitGroup
			for i := range 16 {
				wg.Go(func() {
					message := fmt.Appendf(nil, "message %d", i)
					signature, err := client.SignMessage(rand.Reader, message, tc.opts)
					if err != nil {
						t.Error(err)
						return
					}
					if !tc.verify(message, signature) {
						t.Errorf("signature %d does not verify", i)
					}
				})
			}
			wg.Wait()

			if _, err := client.SignMessage(rand.Reader, []byte("m"), crypto.SHA512); err == nil {
				t.Error("mismatched signer options accepted")
			}
		})
	}
}
