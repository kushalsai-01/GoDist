package cluster

import (
	"context"
	"sync"
	"time"

	"godist/internal/transport"
)

// Election implements a simplified Bully election algorithm:
// - Highest node ID wins.
// - When leader is suspected/dead, start an election.
// - Ask higher IDs if they are alive; if no one responds, become leader.
//
// Learning note:
// - This is NOT Raft.
// - Network partitions can produce split-brain leaders.
type Election struct {
	mu            sync.RWMutex
	leaderID      string
	inElection    bool
	lastHeartbeat time.Time

	selfID string
	mem    *Membership
	cli    *transport.Client
}

func NewElection(selfID string, mem *Membership, cli *transport.Client) *Election {
	return &Election{selfID: selfID, mem: mem, cli: cli}
}

func (e *Election) Leader() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.leaderID
}

func (e *Election) SetLeader(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.leaderID = id
	e.inElection = false
	e.lastHeartbeat = time.Now()
}

func (e *Election) MaybeStart(ctx context.Context) {
	e.mu.Lock()
	if e.inElection {
		e.mu.Unlock()
		return
	}
	e.inElection = true
	e.mu.Unlock()

	go e.run(ctx)
}

func (e *Election) run(ctx context.Context) {
	// Ask higher IDs.
	higher := make([]Member, 0)
	for _, m := range e.mem.HealthyMembers() {
		if m.ID > e.selfID {
			higher = append(higher, m)
		}
	}

	anyOK := false
	for _, m := range higher {
		ctx2, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		_, err := e.cli.PostJSON(ctx2, "http://"+m.Address+"/internal/election", map[string]any{"from_id": e.selfID}, &struct{}{})
		cancel()
		if err == nil {
			anyOK = true
			break
		}
	}

	if anyOK {
		// Someone higher is alive; they should eventually announce leadership.
		e.mu.Lock()
		e.inElection = false
		e.mu.Unlock()
		return
	}

	// Become leader and announce to cluster.
	e.SetLeader(e.selfID)
	for _, m := range e.mem.HealthyMembers() {
		if m.ID == e.selfID {
			continue
		}
		ctx2, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
		_, _ = e.cli.PostJSON(ctx2, "http://"+m.Address+"/internal/leader", map[string]any{"leader_id": e.selfID}, &struct{}{})
		cancel()
	}
}
