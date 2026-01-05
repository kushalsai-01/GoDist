# GoDist

I built GoDist to learn the fundamentals of distributed systems by writing a tiny multi-node key–value store in Go (standard library only). The goal is not scale or production safety — the goal is to make failure modes and trade-offs obvious.

GoDist demonstrates:

- Data ownership via consistent hashing (with virtual nodes)
- Replication (N) and quorum reads/writes (R/W)
- Chaos / fault injection mode to make partial failures observable
- Heartbeats, timeouts, and imperfect failure detection (suspect vs dead)
- A simple leader election (Bully-style, not Raft)


## What I Learned From This

While building this, I repeatedly ran into the same core distributed-systems lessons:

- Ownership is a routing decision, not a “storage location.” A key maps to an owner node, and then replication fans that data out.
- Failure detection is not truth. A timeout only means “I didn’t hear back in time,” which can be caused by overload, GC pauses, or partitions.
- Leader election is easy to implement but hard to make correct under partitions. Without a consensus protocol, split brain is always a risk.
- Consistency is a policy choice. Quorums improve availability and correctness, but they don’t eliminate races or partitions.

- Consistency trade-offs (stale reads, partial failures, partitions)

 ## Learning References

Many of the concepts explored in this project were learned and cross-checked
using public system design resources, especially:

 awesome-system-design-resources

These resources helped with understanding high-level ideas like consistent
hashing, replication, quorums, failure detection, and trade-offs.
All code and implementation decisions in this repository were written manually
to reinforce learning.

## Architecture Overview

ASCII sketch of the system:

```
+------------------+         HTTP          +------------------+
| Node A (coord)   | <-------------------> | Node B           |
| - HTTP server    |                      | - HTTP server    |
| - membership     |                      | - membership     |
| - failure det.   |                      | - failure det.   |
| - election       |                      | - election       |
| - local KV store |                      | - local KV store |
+--------+---------+                      +---------+--------+
         |                                            |
         |                                            |
         |                 HTTP                        |
         +--------------------------------------------+
                          to Node C

Data ownership:

  key --hash--> (consistent hash ring with vnodes) --> primary owner
                                                  \-> next replicas by ring walk

Client flow:

  client -> any node -> coordinator routes to N replicas -> waits for W acks
  client -> any node -> coordinator queries N replicas -> waits for R replies
```

## How It Works

### Key routing (ownership)

Each node maintains a local view of membership. From that view it builds a consistent hash ring with virtual nodes:

- `Owner(key)` picks the primary owner.
- `Walk(key, N)` walks forward on the ring and returns the next **distinct** physical nodes.

When nodes join/leave (or are marked dead), the ring changes and only a minimal fraction of keys move.

### Replication

Each key is stored on `N` nodes:

- Primary is determined by the ring
- Replicas are the next nodes on the ring walk

Writes are sent to all replicas; the write is considered successful once `W` replicas ack.

### Quorum reads and writes

Config:

- `N`: replication factor
- `R`: read quorum
- `W`: write quorum

Rules:

- Write succeeds if at least `W` replicas acknowledge
- Read succeeds if at least `R` replicas respond

Important invariant:

- If `R + W > N`, then a read quorum and a write quorum overlap, which increases the chance you read the latest value.

This project intentionally keeps behavior visible when the invariant is not met.

### Versioning and “latest value”

Each key has a logical version (monotonic counter). To keep versions monotonic across coordinators, the coordinator does:

1) Read the current max version across the replica set
2) Write with `newVersion = max + 1`

Reads choose the value with the highest version.

There is a small best-effort **read repair**: after a quorum read, the coordinator asynchronously pushes the newest value to any stale replicas (including replicas that returned an older version or “not found”).

### Heartbeats and failure handling

Nodes heartbeat each other periodically.

- If a node hasn’t responded for `SuspectAfter`, we mark it `suspect`.
- If it hasn’t responded for `DeadAfter`, we mark it `dead`.

Dead nodes are excluded from routing decisions.

This is a classic timeout-based detector: it can be wrong under partitions.

### Leader election

This project includes a simplified Bully election:

- Highest node ID wins.
- When the current leader is suspected/dead, nodes trigger an election.
- A node asks higher IDs if they’re alive; if none respond, it becomes leader and announces.

Limitations are very intentional:

- Under partition, both sides may elect a leader (split brain).
- There’s no log replication, no state machine replication, and no safety proof.

## Consistency Model

GoDist is **eventually consistent** by default and uses **quorum-based** operations to improve correctness.

- With `R + W > N`, reads are more likely to observe the most recent write.
- With `R + W <= N`, stale reads are expected.

Even with `R + W > N`, partitions and coordinator races can still produce surprising results because this is not a full consensus system.

## Limitations 

It does **not** guarantee:

- Linearizability (strong consistency)
- Partition tolerance with strict safety (no consensus protocol)
- Durable storage across process crashes (store is in-memory)
- Anti-entropy / full background repair for permanently missed replicas
- Secure communication, authentication, or multi-tenant safety

Production systems add consensus (Raft/Paxos), durable logs, richer membership protocols, backpressure, and extensive testing to handle these cases.

## Running the Project

### Build

```bash
go build ./...
```

### Start 3 nodes locally

In three terminals:

```bash
go run ./cmd/godist -id node-1 -addr 127.0.0.1:8081 -N 3 -R 2 -W 2
```

To make failures visible, you can enable chaos mode (random outbound delay/drop, including some failed heartbeats):

```bash
go run ./cmd/godist -id node-1 -addr 127.0.0.1:8081 -N 3 -R 2 -W 2 --chaos
```

```bash
go run ./cmd/godist -id node-2 -addr 127.0.0.1:8082 -seed 127.0.0.1:8081 -N 3 -R 2 -W 2
```

```bash
go run ./cmd/godist -id node-3 -addr 127.0.0.1:8083 -seed 127.0.0.1:8081 -N 3 -R 2 -W 2
```

### Example run (write, kill a node, keep going)

Write a value:

```bash
curl -X POST http://127.0.0.1:8081/kv/put -H "Content-Type: application/json" -d '{"key":"alpha","value":"first"}'
```

Read it from another node:

```bash
curl http://127.0.0.1:8082/kv/get/alpha
```

Kill node-2 (Ctrl+C in its terminal), then write again:

```bash
curl -X POST http://127.0.0.1:8083/kv/put -H "Content-Type: application/json" -d '{"key":"alpha","value":"second"}'
```

Read from node-1:

```bash
curl http://127.0.0.1:8081/kv/get/alpha
```

### PowerShell demo helper

On Windows you can run:

```powershell
./scripts/demo.ps1
```

## Folder Structure

```
cmd/godist/                # single node binary
internal/api/              # HTTP server + coordinator (quorum logic)
internal/cluster/          # membership + failure detector + election
internal/ring/             # consistent hash ring with vnodes
internal/store/            # local in-memory KV with version gating
internal/transport/        # tiny HTTP JSON client helpers
scripts/                   # local demo script
```
