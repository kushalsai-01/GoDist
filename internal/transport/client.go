package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Client struct {
	hc *http.Client

	mu    sync.Mutex
	rng   *rand.Rand
	chaos ChaosConfig

	delayed atomic.Uint64
	dropped atomic.Uint64
	hbFail  atomic.Uint64
}

// ChaosConfig enables deliberate unreliability.
//
// Learning note:
// - This makes partial failures visible.
// - It helps explain timeouts, quorums, and why retries/backoff exist.
// - When disabled, behavior is unchanged.
type ChaosConfig struct {
	Enabled bool

	// DropProb is the default probability of dropping any outbound request.
	DropProb float64

	// HeartbeatDropProb is applied only for requests whose path ends with /heartbeat.
	HeartbeatDropProb float64

	// DelayProb controls whether we add an artificial delay.
	DelayProb float64

	// DelayMin/DelayMax specify the delay range.
	DelayMin time.Duration
	DelayMax time.Duration
}

func DefaultChaosConfig() ChaosConfig {
	return ChaosConfig{
		Enabled:           true,
		DropProb:          0.10,
		HeartbeatDropProb: 0.35,
		DelayProb:         0.30,
		DelayMin:          80 * time.Millisecond,
		DelayMax:          500 * time.Millisecond,
	}
}

var ErrChaosDrop = errors.New("chaos: dropped outbound request")

func NewClient(timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 800 * time.Millisecond
	}
	return &Client{
		hc:  &http.Client{Timeout: timeout},
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (c *Client) EnableChaos(cfg ChaosConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.chaos = cfg
}

type Stats struct {
	Delayed uint64 `json:"delayed"`
	Dropped uint64 `json:"dropped"`
	HBFail  uint64 `json:"heartbeat_failed"`
}

func (c *Client) GetStats() Stats {
	return Stats{
		Delayed: c.delayed.Load(),
		Dropped: c.dropped.Load(),
		HBFail:  c.hbFail.Load(),
	}
}

func (c *Client) maybeChaos(ctx context.Context, rawURL string) error {
	c.mu.Lock()
	cfg := c.chaos
	rng := c.rng
	c.mu.Unlock()

	if !cfg.Enabled {
		return nil
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		// If URL parsing fails, don't introduce chaos.
		return nil
	}

	// Drop decision (special-cased for heartbeats).
	dropProb := cfg.DropProb
	if strings.HasSuffix(parsed.Path, "/heartbeat") {
		dropProb = cfg.HeartbeatDropProb
	}
	if dropProb > 0 {
		c.mu.Lock()
		drop := rng.Float64() < dropProb
		c.mu.Unlock()
		if drop {
			c.dropped.Add(1)
			if strings.HasSuffix(parsed.Path, "/heartbeat") {
				c.hbFail.Add(1)
			}
			return ErrChaosDrop
		}
	}

	// Delay decision.
	if cfg.DelayProb > 0 && cfg.DelayMax > 0 {
		c.mu.Lock()
		delay := rng.Float64() < cfg.DelayProb
		c.mu.Unlock()
		if delay {
			min := cfg.DelayMin
			max := cfg.DelayMax
			if max < min {
				max = min
			}
			jitter := max - min
			d := min
			if jitter > 0 {
				c.mu.Lock()
				d = min + time.Duration(rng.Int63n(int64(jitter)))
				c.mu.Unlock()
			}
			c.delayed.Add(1)
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
			}
		}
	}

	return nil
}

func (c *Client) PostJSON(ctx context.Context, url string, body any, out any) (int, error) {
	if err := c.maybeChaos(ctx, url); err != nil {
		return 0, err
	}
	b, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if out != nil {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if len(data) > 0 {
			if err := json.Unmarshal(data, out); err != nil {
				return resp.StatusCode, err
			}
		}
	}
	return resp.StatusCode, nil
}

func (c *Client) GetJSON(ctx context.Context, url string, out any) (int, error) {
	if err := c.maybeChaos(ctx, url); err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if out == nil {
		return resp.StatusCode, nil
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if len(data) == 0 {
		return resp.StatusCode, errors.New("empty body")
	}
	return resp.StatusCode, json.Unmarshal(data, out)
}

func (c *Client) Delete(ctx context.Context, url string) (int, error) {
	if err := c.maybeChaos(ctx, url); err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}
