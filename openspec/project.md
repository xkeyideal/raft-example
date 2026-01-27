# Project Context

## Purpose
A distributed key-value store demonstration using Hashicorp's Raft consensus algorithm with gRPC for client communication. This project showcases how to build a consistent, replicated state machine using Raft for distributed consensus, with Pebble as the storage engine.

**Core Goals:**
- Demonstrate practical Raft implementation patterns
- Provide linearizable reads and consistent writes across distributed nodes
- Show leader forwarding and cluster management
- Support snapshot and log compaction mechanisms

## Tech Stack
- **Language:** Go 1.22.6
- **Consensus:** Hashicorp Raft (v1.7.0)
- **Storage:** Cockroach Pebble (v2.1.4) via raft-pebbledb v2.0.1
- **RPC:** gRPC (v1.65.0) with Protocol Buffers
- **Logging:** Hashicorp go-hclog (v1.6.3)
- **Cluster Management:** raft-manager for node operations
- **Load Balancing:** grpcbalance for client connections
- **Atomic Operations:** go.uber.org/atomic

## Project Conventions

### Code Style
- **Package naming:** lowercase, no underscores (e.g., `fsm`, `engine`, `service`)
- **Variable naming:** camelCase for locals, PascalCase for exports
- **Constants:** camelCase with descriptive names (e.g., `barrierWriteTimeout`, `grpcConnectTimeout`)
- **Error handling:** explicit error returns, descriptive error wrapping with `fmt.Errorf`
- **File organization:** one primary type per file when possible (e.g., `fsm.go`, `raft.go`)
- **Comments:** document exported functions and complex logic; explain architectural decisions
- **Struct fields:** group related fields, use atomic types for concurrent access

### Architecture Patterns

**Core Architecture:**
- **FSM (Finite State Machine):** Implements Raft FSM interface for deterministic state replication
- **Engine Layer:** Manages Raft lifecycle, leadership, and consistency guarantees
- **Service Layer:** gRPC interface for client operations with leader forwarding
- **Storage Layer:** Pebble key-value store with write batching

**Key Patterns:**
1. **Leader Forwarding:** Non-leader nodes forward write requests to the current leader
2. **Linearizable Reads:** Optional consistent reads through leader verification
3. **Atomic Indexing:** Use `atomic.Uint64` for FSM and applied indices
4. **Observer Pattern:** Raft state changes monitored via observer channel
5. **Barrier Writes:** Ensure leader readiness before serving consistent reads
6. **Dual Port Architecture:** Separate ports for gRPC (40051+) and Raft transport (50051+)

**Directory Structure:**
- `app.go` - Main entry point with CLI flags
- `cmd/` - Command utilities for testing
- `engine/` - Raft setup and cluster management
- `fsm/` - State machine implementation and storage
- `service/` - gRPC service definitions and handlers
- `proto/` - Protocol buffer definitions
- `benchmark/` - Performance testing

### Testing Strategy
- **Benchmarks:** Use Go benchmarks for performance testing (`benchmark/benchmark_test.go`)
- **Load testing:** Parallel write benchmarks with configurable concurrency and batch sizes
- **Manual testing:** `cmd/cmd.go` for integration testing against live cluster
- **Cluster testing:** Use `raft-manager` CLI tool for cluster operations validation
- **Test data:** Random value generation for realistic workload simulation
- **No unit tests currently:** Focus on integration and benchmark testing

### Git Workflow
- Project uses standard GitHub workflow
- Main development on master branch
- Reference external tool: `raft-manager` for cluster administration
- Keep changes focused and atomic when possible

## Domain Context

**Raft Consensus:**
- Every write is a log entry that gets replicated to followers
- Leader election occurs when no heartbeats received within election timeout
- Snapshot creation triggered by `SnapshotInterval` and `SnapshotThreshold`
- Log compaction removes entries before snapshot

**Consistency Levels:**
- **Strong (Linearizable):** Reads go through leader with barrier
- **Eventual (ReadLocal):** Reads from local FSM without coordination

**Cluster Operations:**
1. Bootstrap single node with `--raft_bootstrap`
2. Add voters using `raft-manager` after checking applied_index
3. Leader handles all writes via `Apply()`
4. Automatic failover on leader failure

**Critical Timing Parameters:**
- HeartbeatTimeout: 1000ms
- ElectionTimeout: 1000ms  
- CommitTimeout: 50ms
- LeaderLeaseTimeout: 500ms
- SnapshotInterval: 120s
- SnapshotThreshold: 8192 logs

## Important Constraints

**Technical Constraints:**
- **Apply must run on leader:** Raft's `Apply()` fails on non-leader nodes
- **Deterministic FSM:** State machine must produce identical results on all nodes
- **Server lookup mapping:** Static map currently used for raft→grpc address translation (should use gossip in production)
- **Single cluster:** No multi-region or federation support
- **TCP transport only:** Uses Hashicorp's Raft TCP transport

**Operational Constraints:**
- Initial bootstrap node must be up for cluster formation
- Applied index must be checked before adding new voters
- Minimum 3 nodes recommended for fault tolerance (quorum = n/2 + 1)
- Snapshot retention: 3 most recent snapshots kept

**Data Constraints:**
- Keys and values stored as byte slices
- No built-in authentication or encryption
- No data validation in storage layer

## External Dependencies

**Core Libraries:**
- `github.com/hashicorp/raft` - Raft consensus implementation
- `github.com/xkeyideal/raft-pebbledb` - Pebble-backed Raft log/stable store
- `github.com/cockroachdb/pebble` - LSM-tree based KV store (RocksDB alternative)
- `google.golang.org/grpc` - RPC framework for client communication
- `google.golang.org/protobuf` - Protocol buffer serialization

**Utilities:**
- `github.com/xkeyideal/raft-manager` - External CLI tool for cluster management
- `github.com/xkeyideal/grpcbalance` - Client-side load balancing
- `github.com/iancoleman/strcase` - String case conversion
- `go.uber.org/atomic` - Lock-free atomic operations

**Future Integration Points:**
- Gossip protocol (e.g., Memberlist) for dynamic server discovery
- Metrics and monitoring integration
- TLS/authentication layer
- REST API alongside gRPC
