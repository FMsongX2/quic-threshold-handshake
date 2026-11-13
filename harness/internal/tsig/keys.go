package tsig

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/bytemare/ecc"
	"github.com/bytemare/frost"
	frostdebug "github.com/bytemare/frost/debug"
	trsa "github.com/cloudflare/circl/tss/rsa"
)

// Signature schemes. The plain schemes keep the whole key on one remote signer
// and serve as the (t, n) = (1, 1) baseline.
const (
	SchemeFrost        = "frost"
	SchemeRSA          = "rsa"
	SchemePlainEd25519 = "plain-ed25519"
	SchemePlainRSA     = "plain-rsa"

	rsaBits = 2048
)

// Public is the non-secret key material the coordinator needs.
type Public struct {
	Scheme    string `json:"scheme"`
	Threshold int    `json:"threshold"`
	Signers   int    `json:"signers"`
	// PublicKey is the PKIX encoding of the key in the certificate.
	PublicKey []byte `json:"public_key"`
	// FrostConfig is the encoded frost.Configuration (FROST only).
	FrostConfig []byte `json:"frost_config,omitempty"`
}

// Share is what one signer holds.
type Share struct {
	Public
	ID int `json:"id"`
	// Secret is the encoded key share, or the PKCS #8 key for plain schemes.
	Secret []byte `json:"secret"`
}

func (p *Public) publicKey() (crypto.PublicKey, error) {
	return x509.ParsePKIXPublicKey(p.PublicKey)
}

func (p *Public) frostConfig() (*frost.Configuration, error) {
	conf := new(frost.Configuration)
	if err := conf.Decode(p.FrostConfig); err != nil {
		return nil, err
	}
	return conf, conf.Init()
}

// Deal generates a key for scheme, splits it into n shares with threshold t
// and returns a self-signed certificate for the DNS name "server". The dealer
// sees the whole key; this models a trusted dealer.
func Deal(scheme string, t, n int) (*Public, []*Share, []byte, error) {
	pub := &Public{Scheme: scheme, Threshold: t, Signers: n}
	var secrets [][]byte
	var certSigner crypto.Signer

	switch scheme {
	case SchemePlainEd25519, SchemePlainRSA:
		if t != 1 || n != 1 {
			return nil, nil, nil, errors.New("tsig: plain schemes need t = n = 1")
		}
		var key crypto.Signer
		var err error
		if scheme == SchemePlainEd25519 {
			_, key, err = ed25519.GenerateKey(rand.Reader)
		} else {
			key, err = rsa.GenerateKey(rand.Reader, rsaBits)
		}
		if err != nil {
			return nil, nil, nil, err
		}
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return nil, nil, nil, err
		}
		secrets, certSigner = [][]byte{der}, key

	case SchemeRSA:
		key, err := rsa.GenerateKey(rand.Reader, rsaBits)
		if err != nil {
			return nil, nil, nil, err
		}
		shares, err := trsa.Deal(rand.Reader, uint(n), uint(t), key, true)
		if err != nil {
			return nil, nil, nil, err
		}
		for i := range shares {
			raw, err := shares[i].MarshalBinary()
			if err != nil {
				return nil, nil, nil, err
			}
			secrets = append(secrets, raw)
		}
		certSigner = key

	case SchemeFrost:
		group := ecc.Edwards25519Sha512
		secret := group.NewScalar().Random()
		shares, verificationKey, _ := frostdebug.TrustedDealerKeygen(frost.Ed25519, secret, uint16(t), uint16(n))
		conf := &frost.Configuration{
			Ciphersuite:     frost.Ed25519,
			Threshold:       uint16(t),
			MaxSigners:      uint16(n),
			VerificationKey: verificationKey,
		}
		for _, share := range shares {
			conf.SignerPublicKeyShares = append(conf.SignerPublicKeyShares, share.PublicKeyShare())
			secrets = append(secrets, share.Encode())
		}
		if err := conf.Init(); err != nil {
			return nil, nil, nil, err
		}
		pub.FrostConfig = conf.Encode()
		certSigner = groupKeySigner{secret: secret, public: ed25519.PublicKey(verificationKey.Encode())}

	default:
		return nil, nil, nil, fmt.Errorf("tsig: unknown scheme %q", scheme)
	}

	var err error
	if pub.PublicKey, err = x509.MarshalPKIXPublicKey(certSigner.Public()); err != nil {
		return nil, nil, nil, err
	}
	cert, err := selfSignedCert(certSigner)
	if err != nil {
		return nil, nil, nil, err
	}
	shares := make([]*Share, len(secrets))
	for i, secret := range secrets {
		shares[i] = &Share{Public: *pub, ID: i + 1, Secret: secret}
	}
	return pub, shares, cert, nil
}

// groupKeySigner signs with the undivided FROST group secret. Only the dealer
// uses it, to self-sign the certificate before discarding the secret.
type groupKeySigner struct {
	secret *ecc.Scalar
	public ed25519.PublicKey
}

func (s groupKeySigner) Public() crypto.PublicKey { return s.public }

func (s groupKeySigner) Sign(_ io.Reader, message []byte, _ crypto.SignerOpts) ([]byte, error) {
	frostMu.Lock()
	sig, err := frostdebug.Sign(frost.Ed25519, message, s.secret)
	frostMu.Unlock()
	if err != nil {
		return nil, err
	}
	return append(sig.R.Encode(), sig.Z.Encode()...), nil
}

func selfSignedCert(key crypto.Signer) ([]byte, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 126))
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "server"},
		DNSNames:              []string{"server"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(30 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// WriteDeal stores the dealer output: public.json, cert.pem and one
// share-<id>.json per signer.
func WriteDeal(dir string, pub *Public, shares []*Share, cert []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "public.json"), pub, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), cert, 0o644); err != nil {
		return err
	}
	for _, share := range shares {
		if err := writeJSON(filepath.Join(dir, fmt.Sprintf("share-%d.json", share.ID)), share, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func writeJSON(path string, value any, mode os.FileMode) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), mode)
}

// LoadPublic reads public.json.
func LoadPublic(path string) (*Public, error) {
	pub := new(Public)
	return pub, readJSON(path, pub)
}

// LoadShare reads a share-<id>.json file.
func LoadShare(path string) (*Share, error) {
	share := new(Share)
	return share, readJSON(path, share)
}

func readJSON(path string, value any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, value)
}
