package api

import (
	"context"
	"errors"
	"log"
	"sort"
	"sync"
	"time"

	"godist/internal/cluster"
	"godist/internal/ring"
	"godist/internal/store"
	"godist/internal/transport"
)

// Coordinator is the node that the client contacted.
// It routes requests to the owner + replicas and enforces quorum.
//
// Learning note:
// - Any node can be a coordinator.
// - Coordinators can disagree during partitions.
// - We make version monotonic by reading max version first, then writing max+1.
type Coordinator struct {
	mem *cluster.Membership
	st  *store.Store
	cli *transport.Client

	n int
	r int
	w int

	selfID string

	mu sync.Mutex
}

type ReadValue struct {
	Found   bool
	Value   string
	Version uint64
}

func NewCoordinator(c *cluster.Cluster, st *store.Store, n, r, w int) *Coordinator {
	self := c.Membership().Self()
	return &Coordinator{
		mem: c.Membership(),
		st:  st,
		// Reuse the cluster-wide client so chaos (if enabled) affects quorum traffic too.
		cli:    c.Client(),
		n:      n,
		r:      r,
		w:      w,
		selfID: self.ID,
	}
}

func (c *Coordinator) pickReplicas(key string) (owner ring.Node, replicas []ring.Node, ok bool) {
	r := c.mem.RingSnapshotHealthy()
	o, ok := r.Owner(key)
	if !ok {
		return ring.Node{}, nil, false
	}
	replicas = r.Walk(key, c.n)
	return o, replicas, true
}

func (c *Coordinator) QuorumPut(ctx context.Context, key, value string) (version uint64, ownerID string, err error) {
	owner, replicas, ok := c.pickReplicas(key)
	if !ok {
		return 0, "", errors.New("no nodes in ring")
	}
	ownerID = owner.ID

	// Step 1: read max version across replicas (best-effort, quorum R not required here).
	maxVer := uint64(0)
	for _, n := range replicas {
		v, err := c.internalGet(ctx, n, key)
		if err == nil && v.Found && v.Version > maxVer {
			maxVer = v.Version
		}
	}
	newVer := maxVer + 1

	// Step 2: write to replicas and count acks.
	acks := 0
	var lastErr error
	for _, n := range replicas {
		err := c.internalPut(ctx, n, key, value, newVer)
		if err == nil {
			acks++
			if acks >= c.w {
				// We can return success as soon as quorum is reached.
				// The remaining replicas may be updated later by retries or read-repair.
				return newVer, ownerID, nil
			}
		} else {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("write quorum not reached")
	}
	return 0, ownerID, lastErr
}

func (c *Coordinator) QuorumGet(ctx context.Context, key string) (ReadValue, string, error) {
	owner, replicas, ok := c.pickReplicas(key)
	if !ok {
		return ReadValue{}, "", errors.New("no nodes in ring")
	}
	ownerID := owner.ID

	type resp struct {
		n   ring.Node
		v   ReadValue
		err error
	}

	// Query replicas concurrently; stop once we have R successful responses.
	ch := make(chan resp, len(replicas))
	for _, n := range replicas {
		go func(nn ring.Node) {
			v, err := c.internalGet(ctx, nn, key)
			ch <- resp{n: nn, v: v, err: err}
		}(n)
	}

	oks := make([]resp, 0, len(replicas))
	for i := 0; i < len(replicas); i++ {
		select {
		case <-ctx.Done():
			return ReadValue{}, ownerID, ctx.Err()
		case r := <-ch:
			if r.err == nil {
				oks = append(oks, r)
				if len(oks) >= c.r {
					goto DECIDE
				}
			}
		}
	}

DECIDE:
	if len(oks) < c.r {
		return ReadValue{}, ownerID, errors.New("read quorum not reached")
	}
	// Choose the latest version.
	sort.Slice(oks, func(i, j int) bool { return oks[i].v.Version > oks[j].v.Version })
	best := oks[0].v

	// Best-effort read repair (Dynamo-style): if any replica responded with a stale
	// value (or missing key), asynchronously push the latest version.
	//
	// IMPORTANT: We do not block the client response on repair.
	if best.Found {
		for _, r := range oks {
			if r.v.Found && r.v.Version == best.Version {
				continue
			}
			go func(target ring.Node) {
				ctx2, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
				defer cancel()
				_ = c.internalPut(ctx2, target, key, best.Value, best.Version)
			}(r.n)
		}
	}

	return best, ownerID, nil
}

func (c *Coordinator) internalGet(ctx context.Context, n ring.Node, key string) (ReadValue, error) {
	// Local short-circuit.
	if n.ID == c.selfID {
		v, ok := c.st.Get(key)
		if !ok {
			return ReadValue{Found: false}, nil
		}
		return ReadValue{Found: true, Value: v.Data, Version: v.Version}, nil
	}

	var out InternalGetResponse
	ctx2, cancel := context.WithTimeout(ctx, 900*time.Millisecond)
	defer cancel()
	_, err := c.cli.GetJSON(ctx2, "http://"+n.Address+"/internal/get/"+key, &out)
	if err != nil {
		return ReadValue{}, err
	}
	return ReadValue{Found: out.Found, Value: out.Value, Version: out.Version}, nil
}

func (c *Coordinator) internalPut(ctx context.Context, n ring.Node, key, value string, version uint64) error {
	if n.ID == c.selfID {
		c.st.PutIfNewer(key, value, version)
		return nil
	}

	var out InternalPutResponse
	ctx2, cancel := context.WithTimeout(ctx, 900*time.Millisecond)
	defer cancel()
	_, err := c.cli.PostJSON(ctx2, "http://"+n.Address+"/internal/put", InternalPutRequest{
		Key:     key,
		Value:   value,
		Version: version,
		FromID:  c.selfID,
	}, &out)
	if err != nil {
		return err
	}
	if !out.OK {
		log.Printf("internal put failed node=%s err=%s", n.ID, out.Error)
		return errors.New(out.Error)
	}
	return nil
}
