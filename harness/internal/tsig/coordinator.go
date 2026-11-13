package tsig

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/bytemare/frost"
	trsa "github.com/cloudflare/circl/tss/rsa"
)

// Signer selection policies.
const (
	// PolicyFixed always uses the first t signers in configuration order.
	PolicyFixed = "fixed"
	// PolicyFastest uses the t signers with the lowest estimated RTT.
	PolicyFastest = "fastest"
	// PolicyFirst asks all n signers and uses the first t answers. It needs
	// shares that do not depend on the signer set, so it is RSA only.
	PolicyFirst = "first"
	// PolicyHedge runs a FROST session for every t-subset of the HedgeWidth
	// nearest signers and uses the first one to finish. FROST only.
	PolicyHedge = "hedge"
)

const (
	signTimeout     = 15 * time.Second
	commitPoolSize  = 256
	commitBatchSize = 64
)

// Options configures a Coordinator.
type Options struct {
	Policy    string
	Transport string
	// HedgeWidth is the number of signers PolicyHedge engages, from t to n.
	// Zero means all n.
	HedgeWidth int
	// HedgeDelayFactor, when positive, makes PolicyHedge start with the t
	// nearest signers alone and widen to HedgeWidth only if they have not
	// answered after this multiple of their expected response time.
	HedgeDelayFactor float64
	// HedgeBudget, when positive, caps the share of signatures that may
	// widen after the delay, for example 0.2 for one in five.
	HedgeBudget float64
	// NudgeDelayFactor, when positive, makes the fixed, fastest and first
	// policies ping the signers that have not answered after this multiple
	// of the expected round trip. Persistent transport only.
	NudgeDelayFactor float64
	// Precompute collects FROST nonce commitments ahead of time, which
	// leaves one round trip per signature.
	Precompute bool
	// OnTrace, if set, receives the trace of every signature made through
	// the crypto.Signer interface.
	OnTrace func(*Trace)
}

// Call is one request to one signer.
type Call struct {
	Signer int `json:"signer"`
	// RTTNs runs from just before the request is sent to just after the
	// response is read.
	RTTNs int64 `json:"rtt_ns"`
	// ComputeNs is the handling time the signer reported.
	ComputeNs int64 `json:"compute_ns"`
}

// Round is one set of parallel requests the coordinator waits for.
type Round struct {
	Name   string `json:"name"`
	WallNs int64  `json:"wall_ns"`
	Calls  []Call `json:"calls"`
}

// Trace records where the time of one signature went.
type Trace struct {
	Scheme     string  `json:"scheme"`
	Threshold  int     `json:"threshold"`
	Signers    int     `json:"signers"`
	Policy     string  `json:"policy"`
	Transport  string  `json:"transport"`
	Precompute bool    `json:"precompute"`
	StartNs    int64   `json:"start_unix_ns"`
	TotalNs    int64   `json:"total_ns"`
	Rounds     []Round `json:"rounds"`
	// PoolMiss reports that a precomputed commitment was not available and
	// had to be fetched during the signature.
	PoolMiss bool `json:"pool_miss,omitempty"`
	// HedgeSessions and HedgeShares count the sessions PolicyHedge started
	// and the signature shares it asked signers to compute.
	HedgeSessions int `json:"hedge_sessions,omitempty"`
	HedgeShares   int `json:"hedge_shares,omitempty"`
	// HedgeFired reports that a delayed hedge widened beyond the primary
	// session. HedgeWaitNs is the time the winning session started after
	// the signature began, when that time was not spent fetching
	// commitments.
	HedgeFired  bool  `json:"hedge_fired,omitempty"`
	HedgeWaitNs int64 `json:"hedge_wait_ns,omitempty"`
	// Nudged reports that at least one round pinged a silent signer.
	Nudged bool `json:"nudged,omitempty"`
}

// Coordinator turns key shares on remote signers into signatures. It
// implements crypto.Signer so a TLS stack can use it as a certificate key.
type Coordinator struct {
	pub       *Public
	public    crypto.PublicKey
	opts      Options
	peers     []*peer
	frostConf *frost.Configuration
	// pools holds precomputed commitments per signer, indexed like peers.
	pools []chan *frost.Commitment
	// refills wakes the goroutine that tops up the pool of the same index.
	refills []chan struct{}

	hedgeMu sync.Mutex
	// hedgeTokens is the number of hedges HedgeBudget currently allows.
	hedgeTokens float64
}

// NewCoordinator connects to the signers at addrs, which are listed in signer
// ID order, and measures an initial RTT for each.
func NewCoordinator(pub *Public, addrs []string, opts Options) (*Coordinator, error) {
	if len(addrs) != pub.Signers {
		return nil, fmt.Errorf("tsig: %d signer addresses for %d signers", len(addrs), pub.Signers)
	}
	if opts.Policy == "" {
		opts.Policy = PolicyFixed
	}
	if opts.Transport == "" {
		opts.Transport = TransportPersistent
	}
	if opts.Policy == PolicyFirst && pub.Scheme != SchemeRSA {
		return nil, errors.New("tsig: policy first needs the rsa scheme")
	}
	if opts.Policy == PolicyHedge {
		if pub.Scheme != SchemeFrost {
			return nil, errors.New("tsig: policy hedge needs the frost scheme")
		}
		if opts.HedgeWidth == 0 {
			opts.HedgeWidth = pub.Signers
		}
		if opts.HedgeWidth < pub.Threshold || opts.HedgeWidth > pub.Signers {
			return nil, fmt.Errorf("tsig: hedge width %d outside [%d, %d]", opts.HedgeWidth, pub.Threshold, pub.Signers)
		}
		if opts.HedgeDelayFactor < 0 || opts.HedgeBudget < 0 || opts.HedgeBudget > 1 {
			return nil, errors.New("tsig: hedge delay factor must be >= 0 and hedge budget within [0, 1]")
		}
	}
	if opts.Precompute && pub.Scheme != SchemeFrost {
		return nil, errors.New("tsig: precompute needs the frost scheme")
	}
	public, err := pub.publicKey()
	if err != nil {
		return nil, err
	}
	c := &Coordinator{pub: pub, public: public, opts: opts, hedgeTokens: hedgeBurst}
	if pub.Scheme == SchemeFrost {
		if c.frostConf, err = pub.frostConfig(); err != nil {
			return nil, err
		}
	}
	for i, addr := range addrs {
		c.peers = append(c.peers, &peer{id: i + 1, addr: addr, transport: opts.Transport})
	}

	ctx, cancel := context.WithTimeout(context.Background(), signTimeout)
	defer cancel()
	for _, p := range c.peers {
		for range 3 {
			if _, _, err := p.call(ctx, OpPing, nil); err != nil {
				return nil, fmt.Errorf("tsig: signer %d at %s: %w", p.id, p.addr, err)
			}
		}
	}
	if opts.Precompute {
		for _, p := range c.peers {
			pool := make(chan *frost.Commitment, commitPoolSize)
			wake := make(chan struct{}, 1)
			c.pools = append(c.pools, pool)
			c.refills = append(c.refills, wake)
			for len(pool) <= commitPoolSize/2 {
				if err := c.refill(ctx, p, pool); err != nil {
					return nil, err
				}
			}
			go c.keepFilled(p, pool, wake)
		}
	}
	return c, nil
}

// refill fetches one batch of commitments from p.
func (c *Coordinator) refill(ctx context.Context, p *peer, pool chan *frost.Commitment) error {
	commitments, _, err := c.fetchCommitments(ctx, p, commitBatchSize)
	if err != nil {
		return err
	}
	for _, commitment := range commitments {
		select {
		case pool <- commitment:
		default:
		}
	}
	return nil
}

// keepFilled tops up pool whenever a signature took a commitment from it.
func (c *Coordinator) keepFilled(p *peer, pool chan *frost.Commitment, wake chan struct{}) {
	for range wake {
		for len(pool) <= commitPoolSize/2 {
			ctx, cancel := context.WithTimeout(context.Background(), signTimeout)
			err := c.refill(ctx, p, pool)
			cancel()
			if err != nil {
				time.Sleep(100 * time.Millisecond)
			}
		}
	}
}

func (c *Coordinator) fetchCommitments(ctx context.Context, p *peer, count uint16) ([]*frost.Commitment, Call, error) {
	r, elapsed, err := p.call(ctx, OpFrostCommit, binary.BigEndian.AppendUint16(nil, count))
	call := Call{Signer: p.id, RTTNs: elapsed.Nanoseconds(), ComputeNs: r.computeNs}
	if err != nil {
		return nil, call, err
	}
	if len(r.payload) == 0 || len(r.payload)%int(count) != 0 {
		return nil, call, errors.New("tsig: malformed commitment batch")
	}
	size := len(r.payload) / int(count)
	commitments := make([]*frost.Commitment, count)
	for i := range commitments {
		commitments[i] = new(frost.Commitment)
		if err := commitments[i].Decode(r.payload[i*size : (i+1)*size]); err != nil {
			return nil, call, err
		}
	}
	return commitments, call, nil
}

// Public returns the key in the certificate.
func (c *Coordinator) Public() crypto.PublicKey { return c.public }

// Sign implements crypto.Signer for the two TLS 1.3 signature schemes this
// package supports: ed25519 and rsa_pss_rsae_sha256.
func (c *Coordinator) Sign(_ io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	switch c.pub.Scheme {
	case SchemeFrost, SchemePlainEd25519:
		if opts.HashFunc() != crypto.Hash(0) {
			return nil, errors.New("tsig: Ed25519 signs the unhashed message")
		}
	default:
		if pss, ok := opts.(*rsa.PSSOptions); !ok || pss.Hash != crypto.SHA256 {
			return nil, errors.New("tsig: RSA supports only PSS with SHA-256")
		}
	}
	signature, trace, err := c.SignTraced(context.Background(), digest)
	if err == nil && c.opts.OnTrace != nil {
		c.opts.OnTrace(trace)
	}
	return signature, err
}

// SignTraced signs input, which is the message for Ed25519 schemes and the
// SHA-256 digest for RSA schemes, and verifies the result with the standard
// verifier before returning it.
func (c *Coordinator) SignTraced(ctx context.Context, input []byte) ([]byte, *Trace, error) {
	ctx, cancel := context.WithTimeout(ctx, signTimeout)
	defer cancel()
	trace := &Trace{
		Scheme: c.pub.Scheme, Threshold: c.pub.Threshold, Signers: c.pub.Signers,
		Policy: c.opts.Policy, Transport: c.opts.Transport, Precompute: c.opts.Precompute,
	}
	start := time.Now()
	trace.StartNs = start.UnixNano()

	var signature []byte
	var err error
	switch c.pub.Scheme {
	case SchemePlainEd25519, SchemePlainRSA:
		signature, err = c.signPlain(ctx, input, trace)
	case SchemeRSA:
		signature, err = c.signRSA(ctx, input, trace)
	case SchemeFrost:
		if c.opts.Policy == PolicyHedge {
			signature, err = c.signFrostHedged(ctx, input, trace)
		} else {
			signature, err = c.signFrost(ctx, input, trace)
		}
	}
	if err != nil {
		return nil, nil, err
	}
	if err := c.verify(input, signature); err != nil {
		return nil, nil, err
	}
	trace.TotalNs = time.Since(start).Nanoseconds()
	return signature, trace, nil
}

func (c *Coordinator) verify(input, signature []byte) error {
	switch public := c.public.(type) {
	case ed25519.PublicKey:
		if !ed25519.Verify(public, input, signature) {
			return errors.New("tsig: combined Ed25519 signature does not verify")
		}
	case *rsa.PublicKey:
		return rsa.VerifyPSS(public, crypto.SHA256, input, signature, pssOptions)
	}
	return nil
}

// nearest returns the count signers with the lowest RTT estimate.
func (c *Coordinator) nearest(count int) []*peer {
	peers := append([]*peer(nil), c.peers...)
	sort.SliceStable(peers, func(i, j int) bool { return peers[i].estimate() < peers[j].estimate() })
	return peers[:count]
}

// choose returns the signers a signature will use, per the policy.
func (c *Coordinator) choose() []*peer {
	switch c.opts.Policy {
	case PolicyFirst:
		return c.peers
	case PolicyFastest:
		return c.nearest(c.pub.Threshold)
	}
	return c.peers[:c.pub.Threshold]
}

// stallSlack is added to stall delays to cover signer compute time.
const stallSlack = 2 * time.Millisecond

// stallDelay is how long to wait for peers before treating them as stalled:
// factor times the round trip to the farthest of them, plus stallSlack.
func (c *Coordinator) stallDelay(peers []*peer, factor float64) time.Duration {
	var farthest float64
	for _, p := range peers {
		farthest = max(farthest, p.estimate())
	}
	return time.Duration(factor*farthest) + stallSlack
}

type callResult struct {
	peer  *peer
	reply reply
	call  Call
	err   error
}

// fanOut sends the request built by payload to every peer in parallel and
// returns once need of them answered. Answers that arrive later are dropped.
func (c *Coordinator) fanOut(
	ctx context.Context, name string, peers []*peer, need int, op Op, payload func(*peer) []byte, trace *Trace,
) ([]callResult, error) {
	start := time.Now()
	results := make(chan callResult, len(peers))
	for _, p := range peers {
		go func() {
			r, elapsed, err := p.call(ctx, op, payload(p))
			results <- callResult{
				peer: p, reply: r, err: err,
				call: Call{Signer: p.id, RTTNs: elapsed.Nanoseconds(), ComputeNs: r.computeNs},
			}
		}()
	}
	// A request or response lost on an otherwise idle TCP connection waits
	// for a retransmission timer of about 200 ms. Another packet on the same
	// connection lets TCP notice the gap and retransmit within a round trip,
	// so a ping goes to every signer still silent after the nudge delay.
	var nudge <-chan time.Time
	if c.opts.NudgeDelayFactor > 0 && c.opts.Transport == TransportPersistent {
		nudge = time.After(c.stallDelay(peers, c.opts.NudgeDelayFactor))
	}
	answered := make(map[*peer]bool, len(peers))
	var done []callResult
	var failures []error
	for len(answered) < len(peers) {
		var result callResult
		select {
		case result = <-results:
		case <-nudge:
			nudge = nil
			trace.Nudged = true
			for _, p := range peers {
				if !answered[p] {
					go p.call(ctx, OpPing, nil)
				}
			}
			continue
		}
		answered[result.peer] = true
		if result.err != nil {
			failures = append(failures, fmt.Errorf("signer %d: %w", result.peer.id, result.err))
			if len(peers)-len(failures) < need {
				return nil, fmt.Errorf("tsig: round %s: %w", name, errors.Join(failures...))
			}
			continue
		}
		done = append(done, result)
		if len(done) == need {
			break
		}
	}
	round := Round{Name: name, WallNs: time.Since(start).Nanoseconds()}
	for _, result := range done {
		round.Calls = append(round.Calls, result.call)
	}
	trace.Rounds = append(trace.Rounds, round)
	return done, nil
}

func (c *Coordinator) signPlain(ctx context.Context, input []byte, trace *Trace) ([]byte, error) {
	done, err := c.fanOut(ctx, "sign", c.peers[:1], 1, OpPlainSign, func(*peer) []byte { return input }, trace)
	if err != nil {
		return nil, err
	}
	return done[0].reply.payload, nil
}

func (c *Coordinator) signRSA(ctx context.Context, digest []byte, trace *Trace) ([]byte, error) {
	public := c.public.(*rsa.PublicKey)
	// Every signer must sign the same padded value, so the coordinator picks
	// the PSS salt.
	padder := &trsa.PSSPadder{Rand: rand.Reader, Opts: pssOptions}
	padded, err := padder.Pad(public, crypto.SHA256, digest)
	if err != nil {
		return nil, err
	}
	done, err := c.fanOut(ctx, "share", c.choose(), c.pub.Threshold, OpRSAShare, func(*peer) []byte { return padded }, trace)
	if err != nil {
		return nil, err
	}
	shares := make([]trsa.SignShare, len(done))
	for i, result := range done {
		if err := shares[i].UnmarshalBinary(result.reply.payload); err != nil {
			return nil, err
		}
	}
	return trsa.CombineSignShares(public, uint(c.pub.Signers), uint(c.pub.Threshold), shares, padded)
}

func (c *Coordinator) signFrost(ctx context.Context, message []byte, trace *Trace) ([]byte, error) {
	peers := c.choose()
	list := make(frost.CommitmentList, 0, len(peers))

	var missing []*peer
	if c.opts.Precompute {
		for _, p := range peers {
			select {
			case commitment := <-c.pools[p.id-1]:
				list = append(list, commitment)
			default:
				missing = append(missing, p)
			}
			select {
			case c.refills[p.id-1] <- struct{}{}:
			default:
			}
		}
		trace.PoolMiss = len(missing) > 0
	} else {
		missing = peers
	}
	if len(missing) > 0 {
		one := binary.BigEndian.AppendUint16(nil, 1)
		done, err := c.fanOut(ctx, "commit", missing, len(missing), OpFrostCommit, func(*peer) []byte { return one }, trace)
		if err != nil {
			return nil, err
		}
		for _, result := range done {
			commitment := new(frost.Commitment)
			if err := commitment.Decode(result.reply.payload); err != nil {
				return nil, err
			}
			list = append(list, commitment)
		}
	}
	list.Sort()

	request := encodeFrostSign(message, []frost.CommitmentList{list})
	done, err := c.fanOut(ctx, "sign", peers, len(peers), OpFrostSign, func(*peer) []byte { return request }, trace)
	if err != nil {
		return nil, err
	}
	var shares []*frost.SignatureShare
	for _, result := range done {
		received, err := decodeShares(result.reply.payload, 1)
		if err != nil {
			return nil, err
		}
		shares = append(shares, received...)
	}
	return c.aggregateFrost(message, shares, list)
}

// decodeShares splits a sign response into its count signature shares.
func decodeShares(payload []byte, count int) ([]*frost.SignatureShare, error) {
	if len(payload) == 0 || len(payload)%count != 0 {
		return nil, errors.New("tsig: malformed signature share batch")
	}
	size := len(payload) / count
	shares := make([]*frost.SignatureShare, count)
	for i := range shares {
		shares[i] = new(frost.SignatureShare)
		if err := shares[i].Decode(payload[i*size : (i+1)*size]); err != nil {
			return nil, err
		}
	}
	return shares, nil
}

// aggregateFrost combines the shares of one session into an Ed25519
// signature. Shares are not verified one by one; SignTraced verifies the
// combined signature, which fails if any share is wrong.
func (c *Coordinator) aggregateFrost(message []byte, shares []*frost.SignatureShare, list frost.CommitmentList) ([]byte, error) {
	frostMu.Lock()
	signature, err := c.frostConf.AggregateSignatures(message, shares, list, false)
	frostMu.Unlock()
	if err != nil {
		return nil, err
	}
	return append(signature.R.Encode(), signature.Z.Encode()...), nil
}
