// Package coordd implements the signing daemon that lets QUIC servers in any
// language use a tsig.Coordinator as their certificate key, a client for it,
// and the coordinator flags shared by the commands that build one.
//
// Wire format, over TCP:
//
//	frame    = uint32 big-endian body length | body
//	request  = request id(8) | message
//	response = request id(8) | status(1) | payload
//
// The message is the unhashed input the TLS stack asks the certificate key to
// sign. On success the payload is the signature in the wire form of the TLS
// signature scheme the key type implies: Ed25519, rsa_pss_rsae_sha256 or
// ecdsa_secp256r1_sha256 (ASN.1 DER). On error it is UTF-8 error text.
// Several requests may be in flight on one connection; responses arrive in
// completion order.
package coordd

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// DefaultAddr is the address the daemon listens on unless told otherwise.
const DefaultAddr = "127.0.0.1:7100"

const (
	statusOK  byte = 0
	statusErr byte = 1

	idSize         = 8
	responseHeader = idSize + 1
	// maxFrame bounds allocations driven by a peer; TLS signing inputs are
	// well below one kilobyte.
	maxFrame = 1 << 16
)

func writeFrame(w io.Writer, header, payload []byte) error {
	buf := make([]byte, 4, 4+len(header)+len(payload))
	binary.BigEndian.PutUint32(buf, uint32(len(header)+len(payload)))
	buf = append(buf, header...)
	buf = append(buf, payload...)
	_, err := w.Write(buf)
	return err
}

func readFrame(r io.Reader) ([]byte, error) {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(size[:])
	if n > maxFrame {
		return nil, fmt.Errorf("coordd: frame of %d bytes exceeds limit", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

func writeRequest(w io.Writer, id uint64, message []byte) error {
	return writeFrame(w, binary.BigEndian.AppendUint64(nil, id), message)
}

func parseRequest(body []byte) (uint64, []byte, error) {
	if len(body) < idSize {
		return 0, nil, errors.New("coordd: short request")
	}
	return binary.BigEndian.Uint64(body), body[idSize:], nil
}

func writeResponse(w io.Writer, id uint64, signature []byte, failure error) error {
	header := binary.BigEndian.AppendUint64(make([]byte, 0, responseHeader), id)
	if failure != nil {
		return writeFrame(w, append(header, statusErr), []byte(failure.Error()))
	}
	return writeFrame(w, append(header, statusOK), signature)
}

func parseResponse(body []byte) (uint64, []byte, error) {
	if len(body) < responseHeader {
		return 0, nil, errors.New("coordd: short response")
	}
	id := binary.BigEndian.Uint64(body)
	payload := body[responseHeader:]
	if body[idSize] != statusOK {
		return id, nil, fmt.Errorf("coordd: daemon error: %s", payload)
	}
	return id, payload, nil
}
