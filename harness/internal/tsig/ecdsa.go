package tsig

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/asn1"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"filippo.io/nistec"
	"github.com/coinbase/kryptology/pkg/paillier"
	"github.com/coinbase/kryptology/pkg/tecdsa/gg20/dealer"
	"github.com/coinbase/kryptology/pkg/tecdsa/gg20/participant"
)

// The ecdsa scheme runs the GG20 threshold ECDSA protocol of kryptology over
// P-256 with a trusted dealer. Signers never talk to each other: the
// coordinator relays every protocol message, so each protocol round is one
// round of parallel requests. Rounds 1 to 5 do not depend on the message and
// form the presignature; round 6 signs the digest. A presignature made ahead
// of time adds a delivery step that hands every signer the round 5 messages,
// so that the request of round 6 carries only the digest and fits one packet.
const (
	ecdsaRounds    = 6
	ecdsaDeliver   = ecdsaRounds + 1
	ecdsaSIDSize   = 16
	ecdsaDigestLen = 32

	// ecdsaPoolSize is the number of presignatures the coordinator keeps per
	// signer set. A presigning session costs every signer hundreds of
	// milliseconds of compute, so the coordinator fills the pool before the
	// first signature and refills it with several sessions at once, which
	// lets ecdsaPoolSize/2 signatures run without presigning beside them.
	ecdsaPoolSize       = 256
	ecdsaRefillSessions = 8
	// ecdsaPresignMaxAge bounds how long a presignature waits in the pool,
	// so that it is used well before the signers drop its session.
	ecdsaPresignMaxAge = 12 * time.Hour
	// ecdsaSessionTTL is how long a signer keeps an idle session. Sessions
	// of abandoned signatures and unused presignatures expire after it.
	ecdsaSessionTTL = 24 * time.Hour
)

var p256Order = elliptic.P256().Params().N

// dealECDSA splits a fresh P-256 key with kryptology's GG20 dealer and gives
// every signer a Paillier key. The dealer also creates the ring-Pedersen
// parameters the range proofs use. It returns one encoded
// dealer.ParticipantData per signer and the undivided key, which self-signs
// the certificate.
func dealECDSA(t, n int) ([][]byte, crypto.Signer, error) {
	secret, err := dealer.NewSecret(elliptic.P256())
	if err != nil {
		return nil, nil, err
	}
	key, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), secret.FillBytes(make([]byte, 32)))
	if err != nil {
		return nil, nil, err
	}
	public, shares, err := dealer.NewDealerShares(elliptic.P256(), uint32(t), uint32(n), secret)
	if err != nil {
		return nil, nil, err
	}
	publicShares, err := dealer.PreparePublicShares(shares)
	if err != nil {
		return nil, nil, err
	}

	// Each Paillier key and the proof parameters need two 1024-bit safe
	// primes; generating them dominates the deal, so they run in parallel.
	paillierKeys := make([]*paillier.SecretKey, n)
	var proofParams *dealer.ProofParams
	errs := make([]error, n+1)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { _, paillierKeys[i], errs[i] = paillier.NewKeys() })
	}
	wg.Go(func() { proofParams, errs[n] = dealer.NewProofParams() })
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, nil, err
	}

	encryptKeys := make(map[uint32]*paillier.PublicKey, n)
	for i, sk := range paillierKeys {
		encryptKeys[uint32(i+1)] = &sk.PublicKey
	}
	secrets := make([][]byte, n)
	for i := range n {
		id := uint32(i + 1)
		secrets[i], err = json.Marshal(dealer.ParticipantData{
			Id:             id,
			DecryptKey:     paillierKeys[i],
			SecretKeyShare: shares[id],
			EcdsaPublicKey: public,
			KeyGenType:     dealer.TrustedDealerKeyGenType{ProofParams: proofParams},
			PublicShares:   publicShares,
			EncryptKeys:    encryptKeys,
		})
		if err != nil {
			return nil, nil, err
		}
	}
	return secrets, key, nil
}

// encodeECDSARound builds a request for one round of session sid. Round 1
// carries the IDs of the signer set, rounds 2 to 5 and the delivery step the
// messages of the other signers, and round 6 the digest, followed by the
// round 5 messages unless a delivery step brought them.
func encodeECDSARound(round int, sid [ecdsaSIDSize]byte, body []byte) []byte {
	out := append([]byte{byte(round)}, sid[:]...)
	return append(out, body...)
}

func decodeECDSARound(payload []byte) (int, [ecdsaSIDSize]byte, []byte, error) {
	var sid [ecdsaSIDSize]byte
	if len(payload) < 1+ecdsaSIDSize {
		return 0, sid, nil, errors.New("tsig: malformed ecdsa request")
	}
	copy(sid[:], payload[1:])
	return int(payload[0]), sid, payload[1+ecdsaSIDSize:], nil
}

// ecdsaSession is one signer's state in one signing session.
type ecdsaSession struct {
	mu     sync.Mutex
	signer *participant.Signer
	// next is the next protocol round the session accepts.
	next int
	// round5 holds the other signers' round 5 messages once a delivery
	// step brought them ahead of round 6.
	round5   map[uint32]*participant.Round5Bcast
	lastUsed time.Time
}

// ecdsaSigner answers ecdsa requests. Sessions are independent, so any
// number of them may run at once.
type ecdsaSigner struct {
	info      *dealer.ParticipantData
	threshold int

	mu        sync.Mutex
	sessions  map[[ecdsaSIDSize]byte]*ecdsaSession
	lastSweep time.Time
}

func newECDSASigner(share *Share) (*Signer, error) {
	info := new(dealer.ParticipantData)
	if err := json.Unmarshal(share.Secret, info); err != nil {
		return nil, err
	}
	if info.Id != uint32(share.ID) {
		return nil, fmt.Errorf("tsig: ecdsa share %d holds the key share of signer %d", share.ID, info.Id)
	}
	s := &ecdsaSigner{info: info, threshold: share.Threshold, sessions: make(map[[ecdsaSIDSize]byte]*ecdsaSession)}
	return &Signer{handle: func(op Op, payload []byte) ([]byte, error) {
		if op != OpECDSARound {
			return nil, errUnsupportedOp(op)
		}
		return s.round(payload)
	}}, nil
}

func (s *ecdsaSigner) round(payload []byte) ([]byte, error) {
	round, sid, body, err := decodeECDSARound(payload)
	if err != nil {
		return nil, err
	}
	if round == 1 {
		return s.start(sid, body)
	}
	if round < 2 || round > ecdsaDeliver {
		return nil, fmt.Errorf("tsig: ecdsa round %d out of range", round)
	}
	s.mu.Lock()
	session := s.sessions[sid]
	s.mu.Unlock()
	if session == nil {
		return nil, errors.New("tsig: unknown ecdsa session")
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	// The delivery step goes between rounds 5 and 6.
	protocolRound := round
	if round == ecdsaDeliver {
		protocolRound = ecdsaRounds
	}
	if session.next != protocolRound {
		return nil, fmt.Errorf("tsig: ecdsa session expects round %d, got %d", session.next, round)
	}
	out, err := s.step(session, round, body)
	if err != nil || round == ecdsaRounds {
		// A failed session cannot continue, and a finished one must not
		// sign a second digest with the same nonce.
		s.mu.Lock()
		delete(s.sessions, sid)
		s.mu.Unlock()
	}
	if err != nil {
		return nil, err
	}
	if round != ecdsaDeliver {
		session.next++
	}
	session.lastUsed = time.Now()
	return out, nil
}

// start opens a session for the signer set listed in body and runs round 1.
func (s *ecdsaSigner) start(sid [ecdsaSIDSize]byte, body []byte) ([]byte, error) {
	if len(body) != 2*s.threshold {
		return nil, fmt.Errorf("tsig: ecdsa signer set must list %d signers", s.threshold)
	}
	cosigners := make([]uint32, s.threshold)
	for i := range cosigners {
		cosigners[i] = uint32(binary.BigEndian.Uint16(body[2*i:]))
	}
	if !slices.Contains(cosigners, s.info.Id) {
		return nil, errors.New("tsig: ecdsa signer set does not include this signer")
	}
	if sorted := slices.Sorted(slices.Values(cosigners)); len(slices.Compact(sorted)) != len(cosigners) {
		return nil, errors.New("tsig: ecdsa signer set lists a signer twice")
	}
	signer, err := participant.NewSigner(s.info, cosigners)
	if err != nil {
		return nil, err
	}
	bcast, _, err := signer.SignRound1()
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(bcast)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[sid]; ok {
		return nil, errors.New("tsig: ecdsa session already exists")
	}
	if now.Sub(s.lastSweep) > time.Minute {
		s.lastSweep = now
		for id, session := range s.sessions {
			if session.mu.TryLock() {
				if now.Sub(session.lastUsed) > ecdsaSessionTTL {
					delete(s.sessions, id)
				}
				session.mu.Unlock()
			}
		}
	}
	s.sessions[sid] = &ecdsaSession{signer: signer, next: 2, lastUsed: now}
	return out, nil
}

// step runs rounds 2 to 6 and the delivery step of one session. In the
// trusted dealer setting the library sends no point-to-point messages in
// rounds 1 and 5.
func (s *ecdsaSigner) step(session *ecdsaSession, round int, body []byte) ([]byte, error) {
	signer := session.signer
	switch round {
	case 2:
		in, err := decodeMessages[participant.Round1Bcast](body)
		if err != nil {
			return nil, err
		}
		return marshalOut(signer.SignRound2(in, nil))
	case 3:
		in, err := decodeMessages[participant.P2PSend](body)
		if err != nil {
			return nil, err
		}
		return marshalOut(signer.SignRound3(in))
	case 4:
		in, err := decodeMessages[participant.Round3Bcast](body)
		if err != nil {
			return nil, err
		}
		return marshalOut(signer.SignRound4(in))
	case 5:
		in, err := decodeMessages[participant.Round4Bcast](body)
		if err != nil {
			return nil, err
		}
		bcast, _, err := signer.SignRound5(in)
		return marshalOut(bcast, err)
	case ecdsaDeliver:
		if session.round5 != nil {
			return nil, errors.New("tsig: ecdsa round 5 messages already delivered")
		}
		in, err := decodeMessages[participant.Round5Bcast](body)
		if err != nil {
			return nil, err
		}
		session.round5 = in
		return nil, nil
	}
	if len(body) < ecdsaDigestLen {
		return nil, errors.New("tsig: malformed ecdsa sign request")
	}
	in := session.round5
	if len(body) > ecdsaDigestLen {
		if in != nil {
			return nil, errors.New("tsig: ecdsa round 5 messages already delivered")
		}
		var err error
		if in, err = decodeMessages[participant.Round5Bcast](body[ecdsaDigestLen:]); err != nil {
			return nil, err
		}
	}
	if in == nil {
		return nil, errors.New("tsig: ecdsa sign request without round 5 messages")
	}
	return marshalOut(signer.SignRound6Full(body[:ecdsaDigestLen], in, nil))
}

func decodeMessages[T any](body []byte) (map[uint32]*T, error) {
	var in map[uint32]*T
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, fmt.Errorf("tsig: malformed ecdsa messages: %w", err)
	}
	return in, nil
}

func marshalOut[T any](out T, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

// ecdsaPresign is the coordinator's part of a session that finished rounds 1
// to 5: r, and either the round 5 broadcasts each signer needs in round 6 or
// the fact that they were delivered.
type ecdsaPresign struct {
	sid       [ecdsaSIDSize]byte
	peers     []*peer
	round5    map[int]json.RawMessage
	delivered bool
	r         *big.Int
	made      time.Time
}

// ecdsaPool holds the presignatures of one signer set.
type ecdsaPool struct {
	ready chan *ecdsaPresign
	wake  chan struct{}
}

// signerSetKey identifies a signer set independently of its order.
func signerSetKey(peers []*peer) string {
	ids := make([]int, len(peers))
	for i, p := range peers {
		ids[i] = p.id
	}
	slices.Sort(ids)
	var b strings.Builder
	for _, id := range ids {
		b.WriteString(strconv.Itoa(id))
		b.WriteByte(',')
	}
	return b.String()
}

// ecdsaPoolFor returns the presignature pool of peers, creating it and its
// refill goroutine on first use.
func (c *Coordinator) ecdsaPoolFor(peers []*peer) *ecdsaPool {
	key := signerSetKey(peers)
	c.ecdsaMu.Lock()
	defer c.ecdsaMu.Unlock()
	pool := c.ecdsaPools[key]
	if pool == nil {
		pool = &ecdsaPool{ready: make(chan *ecdsaPresign, ecdsaPoolSize), wake: make(chan struct{}, 1)}
		c.ecdsaPools[key] = pool
		go c.keepPresigned(append([]*peer(nil), peers...), pool)
	}
	return pool
}

// fillPresigns tops up pool to its capacity, running up to
// ecdsaRefillSessions presigning sessions at once.
func (c *Coordinator) fillPresigns(peers []*peer, pool *ecdsaPool) error {
	for {
		missing := cap(pool.ready) - len(pool.ready)
		if missing == 0 {
			return nil
		}
		batch := min(missing, ecdsaRefillSessions)
		results := make(chan error, batch)
		for range batch {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), signTimeout)
				defer cancel()
				pre, err := c.presignECDSA(ctx, peers, true, new(Trace))
				if err == nil {
					select {
					case pool.ready <- pre:
					default:
					}
				}
				results <- err
			}()
		}
		var failures []error
		for range batch {
			failures = append(failures, <-results)
		}
		if err := errors.Join(failures...); err != nil {
			return err
		}
	}
}

// keepPresigned refills pool whenever a signature took a presignature from it
// and it is at most half full.
func (c *Coordinator) keepPresigned(peers []*peer, pool *ecdsaPool) {
	for range pool.wake {
		if len(pool.ready) > ecdsaPoolSize/2 {
			continue
		}
		for c.fillPresigns(peers, pool) != nil {
			time.Sleep(100 * time.Millisecond)
		}
	}
}

// takePresign removes a fresh presignature for peers from its pool without
// waiting, and wakes the refill goroutine.
func (c *Coordinator) takePresign(peers []*peer) *ecdsaPresign {
	pool := c.ecdsaPoolFor(peers)
	defer func() {
		select {
		case pool.wake <- struct{}{}:
		default:
		}
	}()
	for {
		select {
		case pre := <-pool.ready:
			if time.Since(pre.made) < ecdsaPresignMaxAge {
				return pre
			}
		default:
			return nil
		}
	}
}

// presignECDSA runs rounds 1 to 5 with peers, recording them in trace. With
// deliver, it also hands the round 5 messages to the signers.
func (c *Coordinator) presignECDSA(ctx context.Context, peers []*peer, deliver bool, trace *Trace) (*ecdsaPresign, error) {
	pre := &ecdsaPresign{peers: peers}
	if _, err := rand.Read(pre.sid[:]); err != nil {
		return nil, err
	}
	var set []byte
	for _, p := range peers {
		set = binary.BigEndian.AppendUint16(set, uint16(p.id))
	}
	start := encodeECDSARound(1, pre.sid, set)
	round1, err := c.ecdsaRound(ctx, "presign1", peers, func(*peer) []byte { return start }, trace)
	if err != nil {
		return nil, err
	}

	// Round 2 answers with one message per other signer, which round 3
	// delivers to its recipient.
	round2, err := c.ecdsaRound(ctx, "presign2", peers, broadcastFrom(2, pre.sid, round1, nil), trace)
	if err != nil {
		return nil, err
	}
	unicasts := make(map[int]map[int]json.RawMessage, len(peers))
	for from, raw := range round2 {
		var out map[int]json.RawMessage
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("tsig: malformed ecdsa round 2 from signer %d: %w", from, err)
		}
		for to, message := range out {
			if unicasts[to] == nil {
				unicasts[to] = make(map[int]json.RawMessage, len(peers)-1)
			}
			unicasts[to][from] = message
		}
	}
	round3, err := c.ecdsaRound(ctx, "presign3", peers, func(p *peer) []byte {
		body, _ := json.Marshal(unicasts[p.id])
		return encodeECDSARound(3, pre.sid, body)
	}, trace)
	if err != nil {
		return nil, err
	}
	round4, err := c.ecdsaRound(ctx, "presign4", peers, broadcastFrom(4, pre.sid, round3, nil), trace)
	if err != nil {
		return nil, err
	}
	if pre.r, err = ecdsaR(round3, round4); err != nil {
		return nil, err
	}
	if pre.round5, err = c.ecdsaRound(ctx, "presign5", peers, broadcastFrom(5, pre.sid, round4, nil), trace); err != nil {
		return nil, err
	}
	if deliver {
		if _, err := c.ecdsaRound(ctx, "deliver", peers, broadcastFrom(ecdsaDeliver, pre.sid, pre.round5, nil), trace); err != nil {
			return nil, err
		}
		pre.round5, pre.delivered = nil, true
	}
	pre.made = time.Now()
	return pre, nil
}

// ecdsaRound sends one round to every peer and returns the answers by signer
// ID.
func (c *Coordinator) ecdsaRound(
	ctx context.Context, name string, peers []*peer, payload func(*peer) []byte, trace *Trace,
) (map[int]json.RawMessage, error) {
	done, err := c.fanOut(ctx, name, peers, len(peers), OpECDSARound, payload, trace)
	if err != nil {
		return nil, err
	}
	out := make(map[int]json.RawMessage, len(done))
	for _, result := range done {
		out[result.peer.id] = result.reply.payload
	}
	return out, nil
}

// broadcastFrom builds the requests of a round whose input is every other
// signer's previous answer, preceded by prefix.
func broadcastFrom(round int, sid [ecdsaSIDSize]byte, previous map[int]json.RawMessage, prefix []byte) func(*peer) []byte {
	return func(p *peer) []byte {
		others := make(map[int]json.RawMessage, len(previous)-1)
		for id, message := range previous {
			if id != p.id {
				others[id] = message
			}
		}
		body, _ := json.Marshal(others)
		return encodeECDSARound(round, sid, append(append([]byte(nil), prefix...), body...))
	}
}

// ecdsaR computes r = x(R) mod q from the public messages of rounds 3 and 4,
// as every signer does in round 5: R = (Γ_1 + ... + Γ_t) · (δ_1 + ... + δ_t)^-1.
// The signers keep R to themselves, and round 6 returns only their s_i.
func ecdsaR(round3, round4 map[int]json.RawMessage) (*big.Int, error) {
	delta := new(big.Int)
	gamma := nistec.NewP256Point()
	for id, raw := range round3 {
		var share participant.Round3BcastJSON
		if err := json.Unmarshal(raw, &share); err != nil || share.DeltaElement == nil {
			return nil, fmt.Errorf("tsig: malformed ecdsa round 3 from signer %d", id)
		}
		delta.Add(delta, share.DeltaElement)

		var opening participant.Round4Bcast
		if err := json.Unmarshal(round4[id], &opening); err != nil || opening.Witness == nil {
			return nil, fmt.Errorf("tsig: malformed ecdsa round 4 from signer %d", id)
		}
		point, err := nistec.NewP256Point().SetBytes(append([]byte{4}, opening.Witness.Msg...))
		if err != nil {
			return nil, fmt.Errorf("tsig: ecdsa round 4 from signer %d: %w", id, err)
		}
		gamma.Add(gamma, point)
	}
	if delta.ModInverse(delta.Mod(delta, p256Order), p256Order) == nil {
		return nil, errors.New("tsig: ecdsa delta is not invertible")
	}
	bigR, err := gamma.ScalarMult(gamma, delta.FillBytes(make([]byte, 32)))
	if err != nil {
		return nil, err
	}
	x, err := bigR.BytesX()
	if err != nil {
		return nil, fmt.Errorf("tsig: ecdsa R: %w", err)
	}
	r := new(big.Int).SetBytes(x)
	if r.Mod(r, p256Order).Sign() == 0 {
		return nil, errors.New("tsig: ecdsa r is zero")
	}
	return r, nil
}

// signECDSA signs a SHA-256 digest. With Precompute, a pooled presignature
// leaves round 6 as the only round, and its request carries only the digest.
func (c *Coordinator) signECDSA(ctx context.Context, digest []byte, trace *Trace) ([]byte, error) {
	if len(digest) != ecdsaDigestLen {
		return nil, errors.New("tsig: ecdsa signs a 32-byte SHA-256 digest")
	}
	peers := c.choose()
	var pre *ecdsaPresign
	if c.opts.Precompute {
		pre = c.takePresign(peers)
		trace.PoolMiss = pre == nil
	}
	if pre == nil {
		var err error
		if pre, err = c.presignECDSA(ctx, peers, false, trace); err != nil {
			return nil, err
		}
	}

	// ECDSA uses the digest as an integer modulo q, and the library rejects
	// a digest that is not already reduced.
	e := new(big.Int).SetBytes(digest)
	reduced := e.Mod(e, p256Order).FillBytes(make([]byte, ecdsaDigestLen))
	request := broadcastFrom(ecdsaRounds, pre.sid, pre.round5, reduced)
	if pre.delivered {
		digestOnly := encodeECDSARound(ecdsaRounds, pre.sid, reduced)
		request = func(*peer) []byte { return digestOnly }
	}
	shares, err := c.ecdsaRound(ctx, "sign", pre.peers, request, trace)
	if err != nil {
		return nil, err
	}
	s := new(big.Int)
	for id, raw := range shares {
		var share participant.Round6FullBcastJSON
		if err := json.Unmarshal(raw, &share); err != nil || share.SElement == nil {
			return nil, fmt.Errorf("tsig: malformed ecdsa signature share from signer %d", id)
		}
		s.Add(s, share.SElement)
	}
	if s.Mod(s, p256Order).Sign() == 0 {
		return nil, errors.New("tsig: ecdsa s is zero")
	}
	return asn1.Marshal(struct{ R, S *big.Int }{pre.r, s})
}
