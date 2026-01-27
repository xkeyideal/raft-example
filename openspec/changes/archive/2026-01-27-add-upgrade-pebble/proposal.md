## Why

Upgrade Pebble from v1.1.2 to v2.1.4 to benefit from significant performance improvements, bug fixes, and new features. The raft-pebbledb dependency has already upgraded to Pebble v2.1.4, requiring this project to upgrade as well to maintain compatibility. This is a major version upgrade that brings enhanced write amplification, improved iterator performance, and better stability for production workloads.

## What Changes

- Upgrade `github.com/cockroachdb/pebble` dependency from v1.1.2 to v2.1.4 (**MAJOR VERSION UPGRADE**)
- Upgrade `github.com/xkeyideal/raft-pebbledb` to latest version (already using Pebble v2.1.4)
- Verify compatibility with current Raft storage implementation
- Test snapshot creation and restoration with upgraded version
- Validate all FSM operations (Apply, Snapshot, Restore) work correctly
- Run benchmarks to verify performance characteristics

## Capabilities

### New Capabilities

No new capabilities are being introduced. This is a dependency upgrade.

### Modified Capabilities

No existing spec-level requirements are changing. This is an internal implementation change that maintains the same external behavior and API surface.

## Impact

- **Breaking Changes**: Major version upgrade (v1 → v2) may include API changes requiring code modifications
- **Direct Dependencies**: `fsm/store.go` directly uses Pebble APIs that may have changed
- **Raft Integration**: `raft-pebbledb` upgrade required for compatibility
- **Storage Layer**: All FSM storage operations (write batches, snapshots, iterators) need verification
- **Testing**: Comprehensive testing required due to major version upgrade
- **Performance**: Significant improvements expected in write amplification and read performance
- **Compatibility**: Must verify backward compatibility with v1.1.2 on-disk data format
- **Build**: `go.mod` and `go.sum` files will be updated
