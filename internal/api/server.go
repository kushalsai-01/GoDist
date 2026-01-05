package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"godist/internal/cluster"
	"godist/internal/store"
	"godist/internal/transport"
)

type Server struct {
	cluster *cluster.Cluster
	store   *store.Store
	// quorum config
	n int
	r int
	w int

	coordinator *Coordinator

	// Minimal observability: request counters.
	puts      atomic.Uint64
	gets      atomic.Uint64
	joins     atomic.Uint64
	heartbeat atomic.Uint64
	intPut    atomic.Uint64
	intGet    atomic.Uint64
	gossip    atomic.Uint64
	election  atomic.Uint64
	leaderAnn atomic.Uint64
}

func NewServer(c *cluster.Cluster, st *store.Store, n, r, w int) *Server {
	if n <= 0 {
		n = 3
	}
	if r <= 0 {
		r = 2
	}
	if w <= 0 {
		w = 2
	}
	s := &Server{cluster: c, store: st, n: n, r: r, w: w}
	s.coordinator = NewCoordinator(c, st, n, r, w)
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// External client-facing endpoints.
	mux.HandleFunc("/kv/put", s.handlePut)
	mux.HandleFunc("/kv/get/", s.handleGet)
	mux.HandleFunc("/status", s.handleStatus)

	// Membership endpoints.
	mux.HandleFunc("/join", s.handleJoin)
	mux.HandleFunc("/heartbeat", s.handleHeartbeat)

	// Internal endpoints (replication/gossip/election).
	mux.HandleFunc("/internal/put", s.handleInternalPut)
	mux.HandleFunc("/internal/get/", s.handleInternalGet)
	mux.HandleFunc("/internal/gossip", s.handleInternalGossip)
	mux.HandleFunc("/internal/election", s.handleInternalElection)
	mux.HandleFunc("/internal/leader", s.handleInternalLeader)

	return mux
}

func (s *Server) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	// Note: /status is intentionally not "pretty"; it's here to help demos.
	self := s.cluster.Membership().Self()
	members := s.cluster.Membership().Members()
	alive := make([]cluster.Member, 0)
	suspect := make([]cluster.Member, 0)
	dead := make([]cluster.Member, 0)
	for _, m := range members {
		switch m.Health {
		case cluster.HealthAlive:
			alive = append(alive, m)
		case cluster.HealthSuspect:
			suspect = append(suspect, m)
		case cluster.HealthDead:
			dead = append(dead, m)
		}
	}

	resp := map[string]any{
		"self":    self,
		"leader":  s.cluster.Election().Leader(),
		"members": members,
		"health":  map[string]any{"alive": alive, "suspect": suspect, "dead": dead},
		"quorum":  map[string]int{"N": s.n, "R": s.r, "W": s.w},
		"counters": map[string]uint64{
			"external_put":             s.puts.Load(),
			"external_get":             s.gets.Load(),
			"join":                     s.joins.Load(),
			"heartbeat":                s.heartbeat.Load(),
			"internal_put":             s.intPut.Load(),
			"internal_get":             s.intGet.Load(),
			"internal_gossip":          s.gossip.Load(),
			"internal_election":        s.election.Load(),
			"internal_leader_announce": s.leaderAnn.Load(),
		},
		"outbound": map[string]any{"chaos": chaosStats(s.cluster.Client())},
		"now_utc":  time.Now().UTC(),
		"endpoint": self.Address,
	}
	s.writeJSON(w, 200, resp)
}

func chaosStats(c *transport.Client) any {
	if c == nil {
		return nil
	}
	return c.GetStats()
}

func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	s.joins.Add(1)
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req JoinRequest
	if err := s.readJSON(r, &req); err != nil {
		s.writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	// Upsert joining node into our membership view.
	s.cluster.Membership().UpsertMember(cluster.Member{ID: req.ID, Address: req.Address, Epoch: req.Epoch, Health: cluster.HealthAlive, LastOK: time.Now()})
	// Respond with our full snapshot.
	s.writeJSON(w, 200, s.cluster.Membership().Snapshot())
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	s.heartbeat.Add(1)
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req HeartbeatRequest
	if err := s.readJSON(r, &req); err != nil {
		w.WriteHeader(400)
		return
	}
	s.cluster.Membership().MarkHeartbeatOK(req.FromID, req.FromAdr, req.Epoch, time.Now())
	s.writeJSON(w, 200, HeartbeatResponse{OK: true})
}

func (s *Server) handlePut(w http.ResponseWriter, r *http.Request) {
	s.puts.Add(1)
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req PutRequest
	if err := s.readJSON(r, &req); err != nil {
		s.writeJSON(w, 400, PutResponse{OK: false, Error: err.Error()})
		return
	}
	if strings.TrimSpace(req.Key) == "" {
		s.writeJSON(w, 400, PutResponse{OK: false, Error: "key required"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	ver, owner, err := s.coordinator.QuorumPut(ctx, req.Key, req.Value)
	if err != nil {
		s.writeJSON(w, 503, PutResponse{OK: false, Error: err.Error(), Owner: owner})
		return
	}
	s.writeJSON(w, 200, PutResponse{OK: true, Version: ver, Owner: owner})
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	s.gets.Add(1)
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/kv/get/")
	if strings.TrimSpace(key) == "" {
		s.writeJSON(w, 400, GetResponse{Found: false, Error: "key required"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	val, owner, err := s.coordinator.QuorumGet(ctx, key)
	if err != nil {
		s.writeJSON(w, 503, GetResponse{Found: false, Key: key, Error: err.Error(), Owner: owner})
		return
	}
	if !val.Found {
		s.writeJSON(w, 404, GetResponse{Found: false, Key: key, Owner: owner})
		return
	}
	s.writeJSON(w, 200, GetResponse{Found: true, Key: key, Value: val.Value, Version: val.Version, Owner: owner})
}

func (s *Server) handleInternalPut(w http.ResponseWriter, r *http.Request) {
	s.intPut.Add(1)
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req InternalPutRequest
	if err := s.readJSON(r, &req); err != nil {
		s.writeJSON(w, 400, InternalPutResponse{OK: false, Error: err.Error()})
		return
	}
	ok := s.store.PutIfNewer(req.Key, req.Value, req.Version)
	if !ok {
		// Stale write; treat as OK to keep retries idempotent but make it observable in logs.
		log.Printf("stale internal put key=%s version=%d", req.Key, req.Version)
	}
	s.writeJSON(w, 200, InternalPutResponse{OK: true})
}

func (s *Server) handleInternalGet(w http.ResponseWriter, r *http.Request) {
	s.intGet.Add(1)
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/internal/get/")
	v, ok := s.store.Get(key)
	if !ok {
		s.writeJSON(w, 200, InternalGetResponse{Found: false})
		return
	}
	s.writeJSON(w, 200, InternalGetResponse{Found: true, Value: v.Data, Version: v.Version})
}

func (s *Server) handleInternalGossip(w http.ResponseWriter, r *http.Request) {
	s.gossip.Add(1)
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var snap cluster.Snapshot
	if err := s.readJSON(r, &snap); err != nil {
		w.WriteHeader(400)
		return
	}
	s.cluster.Membership().MergeSnapshot(snap)
	w.WriteHeader(200)
}

func (s *Server) handleInternalElection(w http.ResponseWriter, r *http.Request) {
	s.election.Add(1)
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	// If a lower ID asks us, we should start our own election.
	s.cluster.Election().MaybeStart(r.Context())
	w.WriteHeader(200)
}

func (s *Server) handleInternalLeader(w http.ResponseWriter, r *http.Request) {
	s.leaderAnn.Add(1)
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req LeaderAnnounceRequest
	if err := s.readJSON(r, &req); err != nil {
		w.WriteHeader(400)
		return
	}
	s.cluster.Election().SetLeader(req.LeaderID)
	w.WriteHeader(200)
}
