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

// signFrostHedged runs one FROST session for every t-subset of the HedgeWidth
// nearest signers and returns the signature of the first session to finish.
//
// A FROST share is bound to the commitments of one signer set, so shares from
// different sessions cannot be mixed. Every session therefore uses its own
// nonce commitment from each of its signers; no nonce signs twice. A signer
// receives the sessions it belongs to in one request, so the first t signers
// to answer complete a session.
func (c *Coordinator) signFrostHedged(ctx context.Context, message []byte, trace *Trace) ([]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel() // abandons the sessions that did not win
	start := time.Now()
	t := c.pub.Threshold
	members := c.nearest(c.opts.HedgeWidth)
	sessions := combinations(len(members), t)
	perMember := len(sessions) * t / len(members)

	events := make(chan hedgeEvent, len(members)+len(sessions)*t)
	pending := 0

	commits := make([][]*frost.Commitment, len(members))
	commitCall := make([]*Call, len(members))
	commitAt := make([]time.Duration, len(members))
	for i, p := range members {
		if c.opts.Precompute {
			commits[i] = c.takeCommitments(p, perMember)
		}
		missing := perMember - len(commits[i])
		if missing == 0 {
			continue
		}
		trace.PoolMiss = trace.PoolMiss || c.opts.Precompute
		pending++
		go func() {
			fetched, call, err := c.fetchCommitments(ctx, p, uint16(missing))
			events <- hedgeEvent{member: i, commits: fetched, call: call, err: err}
		}()
	}

	// used[i] counts the commitments of member i already given to a session.
	used := make([]int, len(members))
	lists := make([]frost.CommitmentList, len(sessions))
	shares := make([][]*frost.SignatureShare, len(sessions))
	signCalls := make([][]Call, len(sessions))
	started := make([]bool, len(sessions))

	// startReady sends the sign requests of every session whose signers all
	// have their commitments in.
	startReady := func() {
		batch := make([][]int, len(members))
		for s, session := range sessions {
			ready := !started[s]
			for _, i := range session {
				ready = ready && len(commits[i]) == perMember
			}
			if !ready {
				continue
			}
			started[s] = true
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
	startReady()

	var failures []error
	for pending > 0 {
		ev := <-events
		pending--
		if ev.err != nil {
			failures = append(failures, fmt.Errorf("signer %d: %w", members[ev.member].id, ev.err))
			continue
		}
		if ev.sessions == nil {
			commits[ev.member] = append(commits[ev.member], ev.commits...)
			commitCall[ev.member] = &ev.call
			commitAt[ev.member] = time.Since(start)
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
			var commitWall time.Duration
			commitRound := Round{Name: "commit"}
			for _, i := range sessions[s] {
				if commitCall[i] != nil {
					commitRound.Calls = append(commitRound.Calls, *commitCall[i])
					commitWall = max(commitWall, commitAt[i])
				}
			}
			if len(commitRound.Calls) > 0 {
				commitRound.WallNs = commitWall.Nanoseconds()
				trace.Rounds = append(trace.Rounds, commitRound)
			}
			trace.Rounds = append(trace.Rounds, Round{
				Name: "sign", WallNs: (time.Since(start) - commitWall).Nanoseconds(), Calls: signCalls[s],
			})
			return c.aggregateFrost(message, shares[s], lists[s])
		}
	}
	return nil, fmt.Errorf("tsig: no hedged session completed: %w", errors.Join(failures...))
}
