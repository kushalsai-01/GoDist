package cluster

import (
	"context"
	"log"
	"math/rand"
	"sync"
	"time"

	"godist/internal/transport"
)

// Cluster wires together membership + failure detection + leader election.
//
// Learning note:
// - This is purposely "manual" coordination.
// - Lots of real-world edge cases are left visible rather than abstracted away.
type Cluster struct {
	mem   *Membership
	fdCfg FailureDetectorConfig
	cli   *transport.Client
	elec  *Election

	mu     sync.RWMutex
	closed bool
}

func New(selfID, selfAddr string, epoch uint64, vnodes int, cli *transport.Client) *Cluster {
	mem := NewMembership(selfID, selfAddr, epoch, vnodes)
	c := &Cluster{
		mem:   mem,
		fdCfg: DefaultFailureDetectorConfig(),
		cli:   cli,
	}
	c.elec = NewElection(selfID, mem, cli)
	c.elec.SetLeader(selfID) // bootstrap: if alone, I'm the leader.
	return c
}

func (c *Cluster) Membership() *Membership { return c.mem }
func (c *Cluster) Election() *Election     { return c.elec }
func (c *Cluster) Client() *transport.Client {
	return c.cli
}

func (c *Cluster) SetFailureDetectorConfig(cfg FailureDetectorConfig) {
	c.fdCfg = cfg
}

// Join contacts a seed node and merges its membership snapshot.
//
// There is no global coordinator: you can join via any reachable member.
func (c *Cluster) Join(ctx context.Context, seedAddr string) error {
	s := c.mem.Self()
	var out Snapshot
	_, err := c.cli.PostJSON(ctx, "http://"+seedAddr+"/join", map[string]any{
		"id":      s.ID,
		"address": s.Address,
		"epoch":   s.Epoch,
	}, &out)
	if err != nil {
		return err
	}
	c.mem.MergeSnapshot(out)
	return nil
}

func (c *Cluster) Start(ctx context.Context) {
	go c.heartbeatLoop(ctx)
	go c.failureDetectorLoop(ctx)
	go c.gossipLoop(ctx)
}

func (c *Cluster) heartbeatLoop(ctx context.Context) {
	t := time.NewTicker(c.fdCfg.HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			self := c.mem.Self()
			for _, m := range c.mem.Members() {
				if m.ID == self.ID {
					continue
				}
				// We heartbeat even suspects; we stop trying once dead.
				if m.Health == HealthDead {
					continue
				}
				go func(target Member) {
					ctx2, cancel := context.WithTimeout(ctx, c.cliTimeout())
					defer cancel()
					_, err := c.cli.PostJSON(ctx2, "http://"+target.Address+"/heartbeat", map[string]any{
						"from_id":   self.ID,
						"from_addr": self.Address,
						"epoch":     self.Epoch,
					}, &struct{}{})
					if err == nil {
						c.mem.MarkHeartbeatOK(target.ID, target.Address, target.Epoch, time.Now())
					}
				}(m)
			}
		}
	}
}

func (c *Cluster) failureDetectorLoop(ctx context.Context) {
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := time.Now()
			self := c.mem.Self()
			leader := c.elec.Leader()
			for _, m := range c.mem.Members() {
				if m.ID == self.ID {
					continue
				}
				if m.Health == HealthDead {
					continue
				}
				since := now.Sub(m.LastOK)
				if since >= c.fdCfg.DeadAfter {
					c.mem.SetHealth(m.ID, HealthDead)
					if m.ID == leader {
						log.Printf("leader %s declared dead -> start election", leader)
						c.elec.MaybeStart(ctx)
					}
					continue
				}
				if since >= c.fdCfg.SuspectAfter {
					c.mem.SetHealth(m.ID, HealthSuspect)
					if m.ID == leader {
						c.elec.MaybeStart(ctx)
					}
					continue
				}
			}
		}
	}
}

func (c *Cluster) gossipLoop(ctx context.Context) {
	// Best-effort membership sync.
	// Real systems use more robust gossip; this is intentionally simple.
	t := time.NewTicker(1200 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			self := c.mem.Self()
			targets := c.mem.HealthyMembers()
			if len(targets) <= 1 {
				continue
			}
			// Pick a random peer.
			var peer Member
			for {
				peer = targets[rand.Intn(len(targets))]
				if peer.ID != self.ID {
					break
				}
			}
			snap := c.mem.Snapshot()
			ctx2, cancel := context.WithTimeout(ctx, 900*time.Millisecond)
			_, _ = c.cli.PostJSON(ctx2, "http://"+peer.Address+"/internal/gossip", snap, &struct{}{})
			cancel()
		}
	}
}

func (c *Cluster) cliTimeout() time.Duration {
	return 700 * time.Millisecond
}
