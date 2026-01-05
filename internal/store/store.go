package store

import (
	"sync"
	"time"
)

type Value struct {
	Data      string
	Version   uint64
	UpdatedAt time.Time
}

// Store is a tiny in-memory KV.
//
// Learning note:
// - Each node is authoritative only for its local state.
// - Replication happens above this layer.
type Store struct {
	mu sync.RWMutex
	m  map[string]Value
}

func New() *Store {
	return &Store{m: make(map[string]Value)}
}

func (s *Store) Get(key string) (Value, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.m[key]
	return v, ok
}

// PutIfNewer stores the value only if version is newer-or-equal than current.
//
// We accept equal versions as idempotent writes (e.g. retries).
func (s *Store) PutIfNewer(key string, data string, version uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.m[key]
	if ok && cur.Version > version {
		return false
	}
	s.m[key] = Value{Data: data, Version: version, UpdatedAt: time.Now()}
	return true
}
