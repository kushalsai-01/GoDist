package ring

import (
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"sort"
	"sync"
)

// Node is the minimal identity needed by the ring.
// Address is host:port.
// ID must be unique and stable for the lifetime of a node.
type Node struct {
	ID      string
	Address string
}

type vnode struct {
	hash uint64
	node Node
}

// Ring implements consistent hashing with virtual nodes.
//
// Learning note:
// - Virtual nodes smooth out key distribution.
// - Removing/adding nodes only moves keys whose owners are adjacent on the ring.
type Ring struct {
	mu          sync.RWMutex
	vnodes      []vnode
	vnodesPer   int
	nodeByID    map[string]Node
	vnodeHashes map[uint64]struct{}
}

func New(vnodesPerNode int) *Ring {
	if vnodesPerNode <= 0 {
		vnodesPerNode = 16
	}
	return &Ring{
		vnodesPer:   vnodesPerNode,
		nodeByID:    map[string]Node{},
		vnodeHashes: map[uint64]struct{}{},
	}
}

func (r *Ring) Nodes() []Node {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Node, 0, len(r.nodeByID))
	for _, n := range r.nodeByID {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (r *Ring) HasNode(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.nodeByID[id]
	return ok
}

func (r *Ring) AddNode(n Node) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodeByID[n.ID] = n
	r.rebuildLocked()
}

func (r *Ring) RemoveNode(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.nodeByID, id)
	r.rebuildLocked()
}

func (r *Ring) rebuildLocked() {
	r.vnodes = r.vnodes[:0]
	r.vnodeHashes = map[uint64]struct{}{}
	for _, n := range r.nodeByID {
		for i := 0; i < r.vnodesPer; i++ {
			h := hash64(fmt.Sprintf("%s#%d", n.ID, i))
			// Hash collision is extremely unlikely, but we guard anyway to keep behavior deterministic.
			for {
				if _, exists := r.vnodeHashes[h]; !exists {
					break
				}
				h++
			}
			r.vnodeHashes[h] = struct{}{}
			r.vnodes = append(r.vnodes, vnode{hash: h, node: n})
		}
	}
	sort.Slice(r.vnodes, func(i, j int) bool { return r.vnodes[i].hash < r.vnodes[j].hash })
}

// Owner returns the primary owner for key.
// ok=false if the ring is empty.
func (r *Ring) Owner(key string) (n Node, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.vnodes) == 0 {
		return Node{}, false
	}
	h := hash64(key)
	idx := sort.Search(len(r.vnodes), func(i int) bool { return r.vnodes[i].hash >= h })
	if idx == len(r.vnodes) {
		idx = 0
	}
	return r.vnodes[idx].node, true
}

// Walk returns up to k distinct nodes starting at the primary owner of key.
// Uniqueness is based on Node.ID (because virtual nodes repeat physical nodes).
func (r *Ring) Walk(key string, k int) []Node {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if k <= 0 || len(r.vnodes) == 0 {
		return nil
	}
	if k > len(r.nodeByID) {
		k = len(r.nodeByID)
	}

	h := hash64(key)
	start := sort.Search(len(r.vnodes), func(i int) bool { return r.vnodes[i].hash >= h })
	if start == len(r.vnodes) {
		start = 0
	}

	seen := make(map[string]struct{}, k)
	out := make([]Node, 0, k)
	for i := 0; len(out) < k && i < len(r.vnodes); i++ {
		v := r.vnodes[(start+i)%len(r.vnodes)]
		if _, ok := seen[v.node.ID]; ok {
			continue
		}
		seen[v.node.ID] = struct{}{}
		out = append(out, v.node)
	}
	return out
}

func hash64(s string) uint64 {
	sum := sha1.Sum([]byte(s))
	// Take the first 8 bytes to map into uint64.
	return binary.BigEndian.Uint64(sum[:8])
}
