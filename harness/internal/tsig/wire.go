// Package tsig implements the signer service and the coordinator that
// produce TLS-compatible signatures from key shares held by separate servers.
//
// Wire format, over TCP:
//
//	frame    = uint32 big-endian body length | body
//	request  = op(1) | request id(8) | payload
//	response = status(1) | request id(8) | signer compute time in ns(8) | payload
package tsig

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Op identifies a request to a signer.
type Op byte

const (
	// OpPing returns an empty payload. The coordinator uses it to measure RTT.
	OpPing Op = iota + 1
	// OpPlainSign signs with a full private key held by one signer.
	OpPlainSign
	// OpFrostCommit returns count fresh FROST nonce commitments.
	OpFrostCommit
	// OpFrostSign returns a FROST signature share.
	OpFrostSign
	// OpRSAShare returns a threshold RSA signature share.
	OpRSAShare
)

const (
	statusOK  byte = 0
	statusErr byte = 1

	maxFrame       = 1 << 20
	maxCommitBatch = 1024
	requestHeader  = 1 + 8
	responseHeader = 1 + 8 + 8
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
		return nil, fmt.Errorf("tsig: frame of %d bytes exceeds limit", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

func writeRequest(w io.Writer, op Op, id uint64, payload []byte) error {
	var header [requestHeader]byte
	header[0] = byte(op)
	binary.BigEndian.PutUint64(header[1:], id)
	return writeFrame(w, header[:], payload)
}

func parseRequest(body []byte) (Op, uint64, []byte, error) {
	if len(body) < requestHeader {
		return 0, 0, nil, errors.New("tsig: short request")
	}
	return Op(body[0]), binary.BigEndian.Uint64(body[1:]), body[requestHeader:], nil
}

type reply struct {
	computeNs int64
	payload   []byte
}

func writeResponse(w io.Writer, id uint64, computeNs int64, payload []byte, failure error) error {
	var header [responseHeader]byte
	if failure != nil {
		header[0] = statusErr
		payload = []byte(failure.Error())
	}
	binary.BigEndian.PutUint64(header[1:], id)
	binary.BigEndian.PutUint64(header[9:], uint64(computeNs))
	return writeFrame(w, header[:], payload)
}

func parseResponse(body []byte) (uint64, reply, error) {
	if len(body) < responseHeader {
		return 0, reply{}, errors.New("tsig: short response")
	}
	id := binary.BigEndian.Uint64(body[1:])
	payload := body[responseHeader:]
	if body[0] != statusOK {
		return id, reply{}, fmt.Errorf("tsig: signer error: %s", payload)
	}
	return id, reply{computeNs: int64(binary.BigEndian.Uint64(body[9:])), payload: payload}, nil
}
