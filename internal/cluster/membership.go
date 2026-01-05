package cluster

import (
	"sort"
	"sync"
	"time"

	"godist/internal/ring"
)

// Membership holds the local, best-effort view of the cluster.
//
// Learning note:
// - This view can be wrong during partitions.
// - Failure detector is timeout-based and intentionally imperfect.
type Membership struct {
	mu      sync.RWMutex
	self    Member
	members map[string]Member
	ring    *ring.Ring
}

func NewMembership(selfID, selfAddr string, selfEpoch uint64, vnodes int) *Membership {
	m := &Membership{
		self:    Member{ID: selfID, Address: selfAddr, Health: HealthAlive, Epoch: selfEpoch, LastOK: time.Now()},
		members: map[string]Member{},
		ring:    ring.New(vnodes),
	}
	m.members[selfID] = m.self
	m.ring.AddNode(ring.Node{ID: selfID, Address: selfAddr})
	return m
}

func (m *Membership) Self() Member {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.self
}

func (m *Membership) RingSnapshotHealthy() *ring.Ring {
	// Expose a copy-like view by rebuilding a ring from current healthy members.
	m.mu.RLock()
	defer m.mu.RUnlock()
	r := ring.New(16)
	for _, mem := range m.members {
		if mem.Health != HealthDead {
			r.AddNode(ring.Node{ID: mem.ID, Address: mem.Address})
		}
	}
	return r
}

func (m *Membership) Members() []Member {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Member, 0, len(m.members))
	for _, mem := range m.members {
		out = append(out, mem)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (m *Membership) HealthyMembers() []Member {
	ms := m.Members()
	out := make([]Member, 0, len(ms))
	for _, mem := range ms {
		if mem.Health == HealthAlive {
			out = append(out, mem)
		}
	}
	return out
}

func (m *Membership) UpsertMember(in Member) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cur, ok := m.members[in.ID]
	if ok {
		// Epoch guards against stale membership info.
		// A restarted node should bump its epoch to overwrite old address.
		if in.Epoch < cur.Epoch {
			return
		}
		// If epoch is equal, prefer the "best" health based on our detector info.
		// We keep Dead if we decided it is dead; other nodes may still think it's alive.
		if cur.Health == HealthDead {
			in.Health = HealthDead
		}
		// Preserve last OK if we have newer.
		if cur.LastOK.After(in.LastOK) {
			in.LastOK = cur.LastOK
		}
	}

	m.members[in.ID] = in
	m.rebuildRingLocked()
}

func (m *Membership) MarkHeartbeatOK(id string, addr string, epoch uint64, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cur, ok := m.members[id]
	if ok && epoch < cur.Epoch {
		return
	}

	mem := Member{ID: id, Address: addr, Epoch: epoch, Health: HealthAlive, LastOK: now}
	// Keep dead if we already declared dead.
	if ok && cur.Health == HealthDead {
		mem.Health = HealthDead
		mem.LastOK = cur.LastOK
	}
	m.members[id] = mem
	m.rebuildRingLocked()
}

func (m *Membership) SetHealth(id string, h Health) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mem, ok := m.members[id]
	if !ok {
		return
	}
	mem.Health = h
	m.members[id] = mem
	m.rebuildRingLocked()
}

func (m *Membership) Get(id string) (Member, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mem, ok := m.members[id]
	return mem, ok
}

func (m *Membership) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	members := make([]Member, 0, len(m.members))
	for _, mem := range m.members {
		members = append(members, mem)
	}
	sort.Slice(members, func(i, j int) bool { return members[i].ID < members[j].ID })
	return Snapshot{FromID: m.self.ID, FromAddr: m.self.Address, Members: members, Now: time.Now()}
}

func (m *Membership) MergeSnapshot(s Snapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, mem := range s.Members {
		cur, ok := m.members[mem.ID]
		if ok && mem.Epoch < cur.Epoch {
			continue
		}
		if ok && cur.Health == HealthDead {
			mem.Health = HealthDead
			if cur.LastOK.After(mem.LastOK) {
				mem.LastOK = cur.LastOK
			}
		}
		m.members[mem.ID] = mem
	}
	m.rebuildRingLocked()
}

func (m *Membership) rebuildRingLocked() {
	m.ring = ring.New(16)
	for _, mem := range m.members {
		if mem.Health != HealthDead {
			m.ring.AddNode(ring.Node{ID: mem.ID, Address: mem.Address})
		}
	}
}
