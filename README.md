# raft-example

This is some example code for how to use [Hashicorp's Raft implementation](https://github.com/hashicorp/raft) with gRPC.

## Start your own cluster

This example uses Hashicorp's Raft Transport to communicate between nodes using TCP.

This example use two independent ports support application's service and raft's transport.

GRPC Server port: 40051, 40052, 40053, 40054 ...
RAFT Server port: 50051, 50052, 50053, 50054 ...

```shell
$ mkdir /tmp/test
$ mkdir /tmp/test/node{A,B,C}
$ ./raft-example --raft_bootstrap --raft_id=nodeA --grpc_addr=localhost:40051 --raft_addr=localhost:50051 --raft_data_dir /tmp/test
$ ./raft-example --raft_id=nodeB --grpc_addr=localhost:40052 --raft_addr=localhost:50052 --raft_data_dir /tmp/test
$ ./raft-example --raft_id=nodeC --grpc_addr=localhost:40053 --raft_addr=localhost:50053 --raft_data_dir /tmp/test
```

You start up three nodes, and bootstrap one of them. Then you tell the bootstrapped node where to find peers. Those peers sync up to the state of the bootstrapped node and become members of the cluster. Once your cluster is running, you never need to pass `--raft_bootstrap` again.

[raft-manager](https://github.com/xkeyideal/raft-manager) is used to communicate with the cluster and add the other nodes.

add nodes into your cluster, first call `applied_index`, then `add_voter`.

```shell
$ go install github.com/xkeyideal/raft-manager/cmd/manager@latest
$ ./manager localhost:40051 applied_index
$ ./manager localhost:40051 add_voter nodeB localhost:50052 132

$ ./manager localhost:40051 applied_index
$ ./manager localhost:40051 add_voter nodeC localhost:50053 133
```

exec cmd.go test the raft write and read.

```shell
$ go run cmd/cmd.go
```

## Raft

Raft uses logs to synchronize changes. Every change submitted to a Raft cluster is a log entry, which gets stored and replicated to the followers in the cluster. In this example, we use [raft-pebbledb](https://github.com/xkeyideal/raft-pebbledb) to store these logs.
Once in a while Raft decides the logs have grown too large, and makes a snapshot. Your code is asked to write out its state. That state captures all previous logs. Now Raft can delete all the old logs and just use the snapshot. These snapshots are stored using the FileSnapshotStore, which means they'll just be files in your disk.

You can see all this happening in `NewRaft()` in `engine/raft.go`.

## Your FSM

See `fsm/fsm.go`. You'll need to implement a `raft.FSM`, and you probably want a gRPC RPC interface.

## Consistent

The `Apply` method of Hashicorp Raft must be called by leader.

```go
// Apply is used to apply a command to the FSM in a highly consistent
// manner. This returns a future that can be used to wait on the application.
// An optional timeout can be provided to limit the amount of time we wait
// for the command to be started. This must be run on the leader or it
// will fail.
func (r *Raft) Apply(cmd []byte, timeout time.Duration) ApplyFuture {
	return r.ApplyLog(Log{Data: cmd}, timeout)
}
```

So we need forward `Apply` request to the leader. Like the [Consul](https://github.com/hashicorp/consul) KV https://github.com/hashicorp/consul/blob/main/agent/consul/kvs_endpoint.go#L100 by RPC.

We must know the leader RPC address not raft address when want to forward request to leader.

We can use gossip protocol propagate per node RPC, raft address infos and so on. Best practices of [Gossip](https://github.com/xkeyideal/mraft/blob/master/gossip/gossip.go) by [Memberlist](https://github.com/hashicorp/memberlist)

The Raft Example use constant map, you can modify it in https://github.com/xkeyideal/raft-example/blob/master/app.go

```go
var (
	// Default static lookup for backward compatibility
	defaultStaticLookup = map[string]string{
		"127.0.0.1:50051": "127.0.0.1:40051",
		"127.0.0.1:50052": "127.0.0.1:40052",
		"127.0.0.1:50053": "127.0.0.1:40053",
	}
)
```

## Dynamic Service Discovery with Gossip

In production environments, hardcoded address mappings are impractical. This project supports **dynamic service discovery** using [HashiCorp Memberlist](https://github.com/hashicorp/memberlist) (gossip protocol).

### Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                        Address Resolution                        │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│   ┌─────────────────┐     ┌─────────────────────────────────┐   │
│   │ AddressResolver │◄────│ resolver/resolver.go            │   │
│   │   (interface)   │     │ - GetGRPCAddr(nodeID) string    │   │
│   └────────┬────────┘     │ - SetGRPCAddr(nodeID, addr)     │   │
│            │              │ - RemoveAddr(nodeID)            │   │
│     ┌──────┴──────┐       └─────────────────────────────────┘   │
│     │             │                                             │
│     ▼             ▼                                             │
│ ┌───────────┐ ┌───────────┐                                     │
│ │  Static   │ │  Gossip   │                                     │
│ │ Resolver  │ │ Resolver  │                                     │
│ │(hardcoded)│ │(memberlist│                                     │
│ └───────────┘ └─────┬─────┘                                     │
│                     │                                           │
│                     ▼                                           │
│              ┌─────────────┐                                    │
│              │   Gossip    │  gossip/gossip.go                  │
│              │ (memberlist)│  - Node join/leave events          │
│              │             │  - Propagate NodeMeta              │
│              └─────────────┘                                    │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### How It Works

1. **NodeMeta Propagation**: Each node broadcasts its metadata (NodeID, RaftAddr, GRPCAddr) via gossip
2. **Automatic Discovery**: When a node joins/leaves, all other nodes are notified
3. **Leader Forwarding**: When forwarding requests to the leader, the gRPC address is resolved dynamically

### Usage

#### Enable Gossip Mode

```bash
# Node A (bootstrap node)
./raft-example --raft_bootstrap --raft_id=nodeA \
  --grpc_addr=127.0.0.1:40051 --raft_addr=127.0.0.1:50051 \
  --gossip_addr=0.0.0.0:7946 \
  --raft_data_dir /data/raft

# Node B (joins via gossip seeds)
./raft-example --raft_id=nodeB \
  --grpc_addr=127.0.0.1:40052 --raft_addr=127.0.0.1:50052 \
  --gossip_addr=0.0.0.0:7947 --gossip_seeds=127.0.0.1:7946 \
  --raft_data_dir /data/raft

# Node C
./raft-example --raft_id=nodeC \
  --grpc_addr=127.0.0.1:40053 --raft_addr=127.0.0.1:50053 \
  --gossip_addr=0.0.0.0:7948 --gossip_seeds=127.0.0.1:7946,127.0.0.1:7947 \
  --raft_data_dir /data/raft
```

#### Command Line Flags

| Flag | Description | Example |
|------|-------------|---------|
| `--gossip_addr` | Gossip bind address. Empty to disable gossip. | `0.0.0.0:7946` |
| `--gossip_seeds` | Comma-separated list of seed nodes to join | `127.0.0.1:7946,127.0.0.1:7947` |

#### Backward Compatibility

If `--gossip_addr` is not provided, the system falls back to the static `defaultStaticLookup` map (development mode).

### Key Components

#### gossip/gossip.go

```go
// NodeMeta is propagated to all cluster members via gossip
type NodeMeta struct {
    NodeID   string `json:"node_id"`   // Raft server ID
    RaftAddr string `json:"raft_addr"` // Raft transport address
    GRPCAddr string `json:"grpc_addr"` // gRPC service address
}

// Key functions
func New(cfg Config) (*Gossip, error)           // Create gossip instance
func (g *Gossip) GetGRPCAddr(nodeID string) (string, bool) // Resolve address
func (g *Gossip) OnJoin(fn func(NodeMeta))      // Register join callback
func (g *Gossip) OnLeave(fn func(NodeMeta))     // Register leave callback
func (g *Gossip) Leave(timeout time.Duration)   // Gracefully leave cluster
```

#### resolver/resolver.go

```go
// AddressResolver abstracts address resolution for leader forwarding
type AddressResolver interface {
    GetGRPCAddr(nodeID string) (grpcAddr string, ok bool)
    SetGRPCAddr(nodeID, grpcAddr string)
    RemoveAddr(nodeID string)
}

// Two implementations:
// - StaticResolver: uses hardcoded map (development)
// - GossipResolver: uses gossip for dynamic discovery (production)
```

## Linearizable Reads (ReadIndex Style)

This project implements efficient linearizable reads without writing to the Raft log.

### How It Works

Instead of the naive approach (applying read commands through Raft log), we use:

1. **VerifyLeader()**: Contact a quorum to confirm we're still the leader
2. **Barrier Check**: Ensure we've applied all committed entries (via `readyForConsistentReads` flag)
3. **Local Read**: Safe to read locally after the above checks pass

### Code Path

```
Client Request (Linearizable=true)
         │
         ▼
┌─────────────────────────────────┐
│  service/service.go: Get()     │
│  - If not leader: forward      │
│  - If leader: call Query()     │
└─────────────────┬───────────────┘
                  │
                  ▼
┌─────────────────────────────────┐
│  engine/kv.go: Query()         │
│  - ConsistentRead() check      │
│  - ReadLocal() from Pebble     │
└─────────────────┬───────────────┘
                  │
                  ▼
┌─────────────────────────────────┐
│ engine/linearizable.go:        │
│ ConsistentRead()               │
│  - VerifyLeader() → quorum     │
│  - isReadyForConsistentReads() │
└─────────────────────────────────┘
```

### Benefits

| Aspect | Old (Apply-based) | New (ReadIndex-style) |
|--------|-------------------|----------------------|
| Write to log | Yes | No |
| Disk I/O | High | Low |
| Latency | Higher | Lower |
| Consistency | Linearizable | Linearizable |

## Graceful Shutdown

The engine supports graceful shutdown to prevent request loss during restarts.

### Features

- **gRPC GracefulStop**: Waits for in-flight requests to complete (with 10s timeout)
- **Gossip Leave**: Notifies cluster members before shutting down
- **Ordered Cleanup**: gRPC → Gossip → Raft → FSM

### Code

```go
// engine/engine.go: Close()
func (e *Engine) Close() {
    e.shutdownOnce.Do(func() {
        close(e.shutdownCh)
        
        // 1. Gracefully stop gRPC (wait for requests)
        ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer cancel()
        
        done := make(chan struct{})
        go func() {
            e.grpcServer.GracefulStop()
            close(done)
        }()
        
        select {
        case <-done:
            log.Println("[INFO] gRPC server stopped gracefully")
        case <-ctx.Done():
            e.grpcServer.Stop() // Force stop after timeout
        }
        
        // 2. Leave gossip cluster
        if e.gossip != nil {
            e.gossip.Leave(5 * time.Second)
            e.gossip.Shutdown()
        }
        
        // 3. Close Raft and FSM
        e.raft.close()
        e.fsm.Close()
    })
}
```

## Abandon Mechanism (Consul-style Blocking Query Support)

The FSM implements the **abandon mechanism** from Consul, which is essential for implementing blocking/watch-style queries that need to be notified when snapshot restore happens.

### Why It's Needed

When a snapshot restore occurs:
1. The entire FSM state is replaced
2. Indices may have gone backwards
3. Data may have changed significantly
4. Blocking queries waiting on old data become stale

The abandon mechanism solves this by providing a channel that watchers can monitor.

### How It Works

```
┌────────────────────────────────────────────────────────────────┐
│                    Abandon Mechanism Flow                       │
├────────────────────────────────────────────────────────────────┤
│                                                                 │
│  Watcher A ─────┐                                               │
│                 │     ┌─────────────────┐                       │
│  Watcher B ─────┼────►│   AbandonCh()   │◄──── Snapshot Restore │
│                 │     └────────┬────────┘                       │
│  Watcher C ─────┘              │                                │
│                                │ close(oldCh)                   │
│                                ▼                                │
│                   All watchers wake up immediately!             │
│                   → Re-query with index=0                       │
│                   → Refresh cached data                         │
│                                                                 │
└────────────────────────────────────────────────────────────────┘
```

### API

```go
// Get the abandon channel to watch for state restoration
abandonCh := fsm.AbandonCh()

// Watch for abandon in a select
select {
case <-abandonCh:
    // State was restored! Reset and re-query
    lastIndex = 0
    refreshAllCaches()
case <-time.After(timeout):
    // Normal timeout
}
```

### Example: Blocking Query Implementation

```go
// service/blocking_query.go provides production-ready implementations:

// BlockingQuery performs a blocking query with abandon support
result, err := service.BlockingQuery(ctx, fsm, minIndex, timeout, queryFn)
if result.Abandoned {
    // State was restored, retry from beginning
    return retryFromBeginning()
}

// WatchKey continuously watches a key with automatic abandon handling
service.WatchKey(ctx, fsm, func(result *BlockingQueryResult) error {
    if result.Abandoned {
        log.Println("State restored, refreshing...")
        return refreshAllCaches()
    }
    return processUpdate(result.Value)
}, queryFn)
```

### Complete Example

See [examples/abandon_example.go](examples/abandon_example.go) for complete usage patterns including:
- Direct AbandonCh usage
- BlockingQuery for single operations
- WatchKey for continuous monitoring
- Multiple concurrent watchers

## Raft Config Parameters

1. SnapshotInterval & SnapshotThreshold, `SnapshotInterval` controls how often we check if we should perform a snapshot.
   `SnapshotThreshold` controls how many outstanding logs there must be before we perform a snapshot.
   ```go
    // runSnapshots is a long running goroutine used to manage taking
	// new snapshots of the FSM. It runs in parallel to the FSM and
	// main goroutines, so that snapshots do not block normal operation.
	func (r *Raft) runSnapshots() {
		for {
			select {
			case <-randomTimeout(r.config().SnapshotInterval):
				// Check if we should snapshot
				if !r.shouldSnapshot() {
					continue
				}

				// Trigger a snapshot
				if _, err := r.takeSnapshot(); err != nil {
					r.logger.Error("failed to take snapshot", "error", err)
				}

			case future := <-r.userSnapshotCh:
				// User-triggered, run immediately
				id, err := r.takeSnapshot()
				if err != nil {
					r.logger.Error("failed to take snapshot", "error", err)
				} else {
					future.opener = func() (*SnapshotMeta, io.ReadCloser, error) {
						return r.snapshots.Open(id)
					}
				}
				future.respond(err)

			case <-r.shutdownCh:
				return
			}
		}
	}
   ```
2. HeartbeatTimeout, the time in follower state without contact from a leader before we attempt an election.
3. ElectionTimeout, the time in candidate state without contact from a leader before we attempt an election.
4. TrailingLogs, controls how many logs we leave after a snapshot. This is used so that we can quickly replay logs on a follower instead of being forced to send an entire snapshot.

So the most critical parameters of raft for followers sync logs are SnapshotThreshold and TrailingLogs, and the value of SnapshotThreshold should be less than the value of TrailingLogs.

5. NoSnapshotRestoreOnStart, the default to `false`, will restoreSnapshot when `NewRaft`. 

## Inspired

[raft-grpc-example](https://github.com/Jille/raft-grpc-example)

[rqlite](github.com/rqlite/rqlite) is an easy-to-use, lightweight, distributed relational database, which uses SQLite as its storage engine.

[consul](github.com/hashicorp/consul)

## Skills

If you are using this repository inside VS Code with GitHub Copilot skills enabled, the following skill can help you use this project as a real-world Raft reference (leader forwarding, linearizable reads, snapshots, membership changes, etc.):

- [raft-realworld-reference](.github/skills/raft-realworld-reference/SKILL.md)

## License
raft-example is under the BSD 2-Clause License. See the LICENSE file for details.