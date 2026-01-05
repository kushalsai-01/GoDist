package cluster

import "time"

type Health string

const (
	HealthAlive   Health = "alive"
	HealthSuspect Health = "suspect"
	HealthDead    Health = "dead"
)

// Member represents a node in our membership view.
//
// Learning note:
// - In real systems, membership is eventually consistent.
// - Here we keep it simple: best-effort propagation + failure detector.
type Member struct {
	ID      string    `json:"id"`
	Address string    `json:"address"`
	Health  Health    `json:"health"`
	Epoch   uint64    `json:"epoch"` // monotonically increases when a node updates its self-view (restart, addr change)
	LastOK  time.Time `json:"last_ok"`
}

// Snapshot is gossiped during join and periodic sync.
type Snapshot struct {
	FromID   string    `json:"from_id"`
	FromAddr string    `json:"from_addr"`
	Members  []Member  `json:"members"`
	Now      time.Time `json:"now"`
}
