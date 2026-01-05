package api

import "time"

// External client API

type PutRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type PutResponse struct {
	OK      bool   `json:"ok"`
	Version uint64 `json:"version"`
	Owner   string `json:"owner"`
	Error   string `json:"error,omitempty"`
}

type GetResponse struct {
	Found   bool   `json:"found"`
	Key     string `json:"key"`
	Value   string `json:"value,omitempty"`
	Version uint64 `json:"version,omitempty"`
	Owner   string `json:"owner"`
	Error   string `json:"error,omitempty"`
}

// Internal replication API

type InternalPutRequest struct {
	Key     string `json:"key"`
	Value   string `json:"value"`
	Version uint64 `json:"version"`
	FromID  string `json:"from_id"`
}

type InternalPutResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type InternalGetResponse struct {
	Found   bool   `json:"found"`
	Value   string `json:"value,omitempty"`
	Version uint64 `json:"version,omitempty"`
}

// Membership / coordination

type JoinRequest struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Epoch   uint64 `json:"epoch"`
}

type JoinResponse struct {
	OK      bool      `json:"ok"`
	Members []any     `json:"members"` // decoded by cluster package on the other side; keep API decoupled
	Now     time.Time `json:"now"`
	Error   string    `json:"error,omitempty"`
}

type HeartbeatRequest struct {
	FromID  string `json:"from_id"`
	FromAdr string `json:"from_addr"`
	Epoch   uint64 `json:"epoch"`
}

type HeartbeatResponse struct {
	OK bool `json:"ok"`
}

type ElectionRequest struct {
	FromID string `json:"from_id"`
}

type ElectionResponse struct {
	OK bool `json:"ok"`
}

type LeaderAnnounceRequest struct {
	LeaderID string `json:"leader_id"`
}
