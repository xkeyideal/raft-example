## 1. Preparation

- [x] 1.1 Verify Pebble v2.1.4 is the target version
- [x] 1.2 Review Pebble v2.0 release notes and migration guide
- [x] 1.3 Review Pebble v2.1.x changelogs for breaking changes
- [x] 1.4 Check raft-pebbledb latest version and its Pebble dependency
- [x] 1.5 Identify breaking API changes affecting fsm/store.go
- [x] 1.6 Run baseline benchmark suite and save results for comparison

## 2. Dependency Updates

- [x] 2.1 Update Pebble to v2.1.4: `go get github.com/cockroachdb/pebble@v2.1.4`
- [x] 2.2 Update raft-pebbledb to latest: `go get github.com/xkeyideal/raft-pebbledb@latest`
- [x] 2.3 Verify both dependencies use compatible Pebble version in go.mod
- [x] 2.4 Run `go mod tidy` to clean up dependencies
- [x] 2.5 Review all go.mod and go.sum changes
- [x] 2.6 Check for any indirect dependency conflicts

## 3. Code Updates and Build Verification

- [x] 3.1 Update fsm/store.go for Pebble v2 API changes (if any)
- [x] 3.2 Update any other files using Pebble APIs directly
- [x] 3.3 Build the project: `go build`
- [x] 3.4 Fix any compilation errors from v2 breaking changes
- [x] 3.5 Review and address deprecation warnings
- [x] 3.6 Update Pebble API usage to v2 best practices

## 4. Testing

- [x] 4.1 Run unit tests: `go test ./...`
- [x] 4.2 Test backward compatibility: open existing v1.1.2 data files with v2.1.4
- [x] 4.3 Test FSM snapshot creation on existing v1.1.2 data
- [x] 4.4 Test FSM snapshot restoration with v2.1.4 created snapshots
- [x] 4.5 Run benchmark suite: `cd benchmark && go test -bench=. -benchmem`
- [x] 4.6 Compare benchmark results with v1.1.2 baseline
- [x] 4.7 Document any performance improvements or regressions

## 5. Integration Testing

- [x] 5.1 Start a single-node Raft cluster with upgraded version
- [x] 5.2 Perform write operations and verify they succeed
- [x] 5.3 Perform read operations and verify correctness
- [x] 5.4 Test snapshot creation and log compaction
- [x] 5.5 Test cluster restart and data persistence

## 6. Documentation

- [x] 6.1 Document performance changes from v1.1.2 to v2.1.4
- [x] 6.2 Document any API changes made in fsm/store.go
- [x] 6.3 Update project.md with new Pebble v2.1.4 and raft-pebbledb versions
- [x] 6.4 Note any breaking changes or migration considerations
- [x] 6.5 Document new v2 features available for future use
- [x] 6.6 Update README if minimum version requirements changed
