## Context

Currently using Pebble v1.1.2 (released ~1 year ago) as the storage engine for the Raft FSM. The storage layer is accessed through:
- Direct Pebble API usage in `fsm/store.go`
- Raft-Pebble integration via `github.com/xkeyideal/raft-pebbledb` for Raft log storage

Pebble has had multiple releases since v1.1.2 with performance improvements and bug fixes. Need to upgrade while maintaining backward compatibility with existing on-disk data.

**Current Usage:**
- Write batches for atomic FSM updates
- Snapshots for Raft snapshot creation/restoration
- Iterators for range queries
- Manual compaction controls

## Goals / Non-Goals

**Goals:**
- Upgrade to Pebble v2.1.4 (major version upgrade from v1.1.2)
- Upgrade raft-pebbledb to latest version (already using Pebble v2.1.4)
- Maintain backward compatibility with existing v1.1.2 data files
- Verify all FSM operations work correctly with v2 API
- Handle any breaking API changes from v1 to v2
- Quantify performance changes through benchmarks

**Non-Goals:**
- Changing FSM API or behavior
- Migrating data format or schema
- Adding new Pebble features not currently used
- Optimizing Pebble configuration settings (separate effort)

## Decisions

### Decision 1: Upgrade to Pebble v2.1.4

**Choice:** Upgrade to Pebble v2.1.4 (major version upgrade)

**Rationale:**
- raft-pebbledb dependency already uses Pebble v2.1.4, requiring this upgrade for compatibility
- v2.x includes significant performance improvements and bug fixes accumulated over time
- Major version change requires careful API compatibility review
- v2.1.4 is a stable release with production testing

**Alternatives Considered:**
- Stay on v1.1.2: Not viable - incompatible with upgraded raft-pebbledb
- Upgrade to v1.x latest: Would still have version mismatch with raft-pebbledb
- Delay upgrade: Would prevent using latest raft-pebbledb features and fixes

### Decision 2: Upgrade raft-pebbledb to Latest

**Choice:** Upgrade `raft-pebbledb` to latest version alongside Pebble v2.1.4

**Rationale:**
- raft-pebbledb latest version already uses Pebble v2.1.4
- Both dependencies must be upgraded together to maintain compatibility
- Latest raft-pebbledb includes bug fixes and improvements for Raft log storage
- Coordinated upgrade prevents version mismatch issues

**Alternatives Considered:**
- Only upgrade Pebble: Would cause version conflicts with raft-pebbledb
- Keep old raft-pebbledb: Would prevent Pebble v2 upgrade entirely

### Decision 3: Compatibility Testing Strategy

**Choice:** Multi-step validation approach:
1. Run existing benchmark suite
2. Test snapshot creation/restoration on existing data
3. Verify all FSM methods (Apply, Snapshot, Restore)
4. Check build and test suite passes

**Rationale:**
- Pebble maintains on-disk format compatibility within major versions
- Our usage is straightforward (no advanced features)
- Benchmarks will reveal any performance regressions

## Risks / Trade-offs

**[Risk] Performance regression in specific workloads**
→ **Mitigation:** Run full benchmark suite before/after and compare results. Document any significant changes.

**[Risk] Backward incompatibility with existing data files**
→ **Mitigation:** Pebble v1.x maintains format compatibility. Test snapshot restore with existing data. Have rollback plan ready.

**[Risk] Breaking API changes in v2.x requiring code modifications**
→ **Mitigation:** Thoroughly review Pebble v2.0 and v2.1 changelogs for breaking changes. Review migration guide. Update fsm/store.go as needed.

**[Risk] Raft-Pebble adapter version conflicts**
→ **Mitigation:** Upgrade raft-pebbledb to latest version simultaneously. Verify both use compatible Pebble v2.1.4.

**[Risk] Data format incompatibility between v1.1.2 and v2.1.4**
→ **Mitigation:** Pebble generally maintains backward compatibility. Test with existing data extensively. Have migration plan if needed.

**[Trade-off] Testing time vs upgrade benefit**
- Testing thoroughly takes time but ensures stability
- Benefits (performance, bug fixes) justify the effort

## Migration Plan

### Upgrade Steps

1. **Review Pebble v2 migration guide:**
   - Check official Pebble v2.0 release notes and migration documentation
   - Identify breaking API changes affecting our code

2. **Update dependencies:**
   ```bash
   go get github.com/cockroachdb/pebble@v2.1.4
   go get github.com/xkeyideal/raft-pebbledb@latest
   ```

3. **Verify dependency versions:**
   - Confirm both packages use compatible Pebble v2.1.4
   - Check go.mod for any version conflicts

3. **Run tests:**
   ```bash
   go test ./...
   ```

4. **Run benchmarks:**
   ```bash
   cd benchmark && go test -bench=. -benchmem
   ```

5. **Compare results:**
   - Document any significant performance changes
   - Ensure no regressions in critical operations

6. **Integration testing:**
   - Test with existing Raft cluster
   - Verify snapshot creation/restoration
   - Test node join/leave operations

### Rollback Strategy

If issues are discovered:
1. Revert go.mod to previous versions
2. Run `go mod tidy`
3. Rebuild and redeploy

On-disk data remains compatible so no data migration needed for rollback.

## Open Questions

1. **What are the specific breaking changes in Pebble v2.x API?**
   - Need to review v2.0, v2.1 changelogs and migration guide
   - Identify which changes affect fsm/store.go code

2. **Does v2.1.4 maintain backward compatibility with v1.1.2 data format?**
   - Verify through testing with existing data
   - Check if any migration steps are required
