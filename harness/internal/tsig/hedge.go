package tsig

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bytemare/frost"
)

// combinations returns every k-element subset of {0, ..., n-1}.
func combinations(n, k int) [][]int {
	var out [][]int
	idx := make([]int, k)
	for i := range idx {
		idx[i] = i
	}
	for {
		out = append(out, append([]int(nil), idx...))
		i := k - 1
		for i >= 0 && idx[i] == n-k+i {
			i--
		}
		if i < 0 {
			return out
		}
		idx[i]++
		for j := i + 1; j < k; j++ {
			idx[j] = idx[j-1] + 1
		}
	}
}

// takeCommitments removes up to count precomputed commitments of p from its
// pool without waiting.
func (c *Coordinator) takeCommitments(p *peer, count int) []*frost.Commitment {
	var taken []*frost.Commitment
	for len(taken) < count {
		select {
		case commitment := <-c.pools[p.id-1]:
			taken = append(taken, commitment)
			continue
		default:
		}
		break
	}
	select {
	case c.refills[p.id-1] <- struct{}{}:
	default:
	}
	return taken
}

type hedgeEvent struct {
	member int
	// sessions lists the sessions a sign response answers; nil marks a
	// commit response.
	sessions []int
	commits  []*frost.Commitment
	reply    reply
	call     Call
	err      error
}

// hedgeSlack is added to the hedge delay to cover signer compute time.
const hedgeSlack = 2 * time.Millisecond

// hedgeBurst caps the hedges a coordinator may save up under HedgeBudget.
const hedgeBurst = 10

// hedgeDelay is how long a delayed hedge waits for the primary session:
// HedgeDelayFactor times the round trips the primary needs to its farthest
// signer, plus hedgeSlack.
func (c *Coordinator) hedgeDelay(primary []*peer) time.Duration {
	var farthest float64
	for _, p := range primary {
		farthest = max(farthest, p.estimate())
	}
	rounds := 2.0
	if c.opts.Precompute {
		rounds = 1
	}
	return time.Duration(c.opts.HedgeDelayFactor*rounds*farthest) + hedgeSlack
}

// earnHedge credits one signature toward the hedge budget.
func (c *Coordinator) earnHedge() {
	c.hedgeMu.Lock()
	defer c.hedgeMu.Unlock()
	c.hedgeTokens = min(c.hedgeTokens+c.opts.HedgeBudget, hedgeBurst)
}

// spendHedge reports whether the budget allows one more hedge and charges it.
func (c *Coordinator) spendHedge() bool {
	if c.opts.HedgeBudget == 0 {
		return true
	}
	c.hedgeMu.Lock()
	defer c.hedgeMu.Unlock()
	if c.hedgeTokens < 1 {
		return false
	}
	c.hedgeTokens--
	return true
}

// signFrostHedged runs FROST sessions over the t-subsets of the HedgeWidth
// nearest signers and returns the signature of the first session to finish.
//
// A FROST share is bound to the commitments of one signer set, so shares from
// different sessions cannot be mixed. Every session therefore uses its own
// nonce commitment from each of its signers; no nonce signs twice. A signer
// receives the sessions it belongs to in one request, so the first t signers
// to answer complete a session.
//
// With HedgeDelayFactor zero every session starts at once. Otherwise only the
// primary session, the t nearest signers, starts; the others follow if the
// primary has not finished after hedgeDelay and the budget allows, or as soon
// as a signer reports an error.
func (c *Coordinator) signFrostHedged(ctx context.Context, message []byte, trace *Trace) ([]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel() // abandons the sessions that did not win
	start := time.Now()
	t := c.pub.Threshold
	members := c.nearest(c.opts.HedgeWidth)
	// sessions[0] is the primary: members are ordered nearest first.
	sessions := combinations(len(members), t)
	perMember := len(sessions) * t / len(members)
	c.earnHedge()

	events := make(chan hedgeEvent, 2*len(members)+len(sessions)*t)
	pending := 0
	commits := make([][]*frost.Commitment, len(members))
	commitCall := make([]*Call, len(members))

	// acquire obtains count more commitments of member i, from the pool when
	// it has them and from the signer otherwise.
	acquire := func(i, count int) {
		if c.opts.Precompute {
			taken := c.takeCommitments(members[i], count)
			commits[i] = append(commits[i], taken...)
			count -= len(taken)
			trace.PoolMiss = trace.PoolMiss || count > 0
		}
		if count == 0 {
			return
		}
		pending++
		go func() {
			fetched, call, err := c.fetchCommitments(ctx, members[i], uint16(count))
			events <- hedgeEvent{member: i, commits: fetched, call: call, err: err}
		}()
	}

	// used[i] counts the commitments of member i already given to a session.
	used := make([]int, len(members))
	lists := make([]frost.CommitmentList, len(sessions))
	shares := make([][]*frost.SignatureShare, len(sessions))
	signCalls := make([][]Call, len(sessions))
	startedAt := make([]time.Duration, len(sessions))
	started := make([]bool, len(sessions))
	// open is the number of sessions, counted from the primary, allowed to
	// start.
	open := len(sessions)

	// startReady sends the sign requests of every open session whose signers
	// all have an unused commitment.
	startReady := func() {
		batch := make([][]int, len(members))
		for s, session := range sessions[:open] {
			ready := !started[s]
			for _, i := range session {
				ready = ready && used[i] < len(commits[i])
			}
			if !ready {
				continue
			}
			started[s] = true
			startedAt[s] = time.Since(start)
			trace.HedgeSessions++
			for _, i := range session {
				lists[s] = append(lists[s], commits[i][used[i]])
				used[i]++
				batch[i] = append(batch[i], s)
			}
			lists[s].Sort()
		}
		for i, ready := range batch {
			if len(ready) == 0 {
				continue
			}
			request := make([]frost.CommitmentList, len(ready))
			for k, s := range ready {
				request[k] = lists[s]
			}
			trace.HedgeShares += len(ready)
			pending++
			go func() {
				r, elapsed, err := members[i].call(ctx, OpFrostSign, encodeFrostSign(message, request))
				events <- hedgeEvent{
					member: i, sessions: ready, reply: r, err: err,
					call: Call{Signer: members[i].id, RTTNs: elapsed.Nanoseconds(), ComputeNs: r.computeNs},
				}
			}()
		}
	}

	var timer <-chan time.Time
	if c.opts.HedgeDelayFactor > 0 && len(sessions) > 1 {
		open = 1
		for i := range t {
			acquire(i, 1)
		}
		timer = time.After(c.hedgeDelay(members[:t]))
	} else {
		for i := range members {
			acquire(i, perMember)
		}
	}
	// widen opens the sessions beyond the primary.
	widen := func() {
		timer = nil
		trace.HedgeFired = true
		open = len(sessions)
		for i := range members {
			if i < t {
				acquire(i, perMember-1)
			} else {
				acquire(i, perMember)
			}
		}
		startReady()
	}
	startReady()

	var failures []error
	for pending > 0 || timer != nil {
		var ev hedgeEvent
		select {
		case ev = <-events:
			pending--
		case <-timer:
			timer = nil
			if c.spendHedge() {
				widen()
			}
			continue
		}
		if ev.err != nil {
			failures = append(failures, fmt.Errorf("signer %d: %w", members[ev.member].id, ev.err))
			if timer != nil {
				widen() // the primary cannot finish; do not wait for the timer
			}
			continue
		}
		if ev.sessions == nil {
			commits[ev.member] = append(commits[ev.member], ev.commits...)
			commitCall[ev.member] = &ev.call
			startReady()
			continue
		}
		received, err := decodeShares(ev.reply.payload, len(ev.sessions))
		if err != nil {
			return nil, err
		}
		for k, s := range ev.sessions {
			shares[s] = append(shares[s], received[k])
			signCalls[s] = append(signCalls[s], ev.call)
			if len(shares[s]) < t {
				continue
			}
			// Time before the winning session started is the commit round
			// when its signers were asked for commitments, and hedge wait
			// otherwise.
			commitRound := Round{Name: "commit", WallNs: startedAt[s].Nanoseconds()}
			for _, i := range sessions[s] {
				if commitCall[i] != nil {
					commitRound.Calls = append(commitRound.Calls, *commitCall[i])
				}
			}
			if len(commitRound.Calls) > 0 {
				trace.Rounds = append(trace.Rounds, commitRound)
			} else {
				trace.HedgeWaitNs = startedAt[s].Nanoseconds()
			}
			trace.Rounds = append(trace.Rounds, Round{
				Name: "sign", WallNs: (time.Since(start) - startedAt[s]).Nanoseconds(), Calls: signCalls[s],
			})
			return c.aggregateFrost(message, shares[s], lists[s])
		}
	}
	return nil, fmt.Errorf("tsig: no hedged session completed: %w", errors.Join(failures...))
}
