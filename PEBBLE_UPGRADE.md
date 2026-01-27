# Pebble v2 Upgrade Summary

## Upgrade Details

- **Pebble:** v1.1.2 → v2.1.4
- **raft-pebbledb:** v1.7.0 → v2.0.1
- **Date:** 2026-01-27

## Breaking Changes

### 1. Module Path Change
Pebble v2 uses a new module path with `/v2` suffix:
```go
// Old (v1)
import "github.com/cockroachdb/pebble"

// New (v2)
import pebble "github.com/cockroachdb/pebble/v2"
```

Similarly, raft-pebbledb v2 uses `/v2` suffix:
```go
// Old (v1)
import pebbledb "github.com/xkeyideal/raft-pebbledb"

// New (v2)
import pebbledb "github.com/xkeyideal/raft-pebbledb/v2"
```

### 2. Logger Interface Change
The `pebble.Logger` interface now requires an `Errorf` method:

```go
type Logger struct{}

func (log *Logger) Infof(format string, args ...interface{}) {
	fmt.Printf(format, args...)
}

// New method required in v2
func (log *Logger) Errorf(format string, args ...interface{}) {
	fmt.Printf(format, args...)
}

func (log *Logger) Fatalf(format string, args ...interface{}) {
	fmt.Printf(format, args...)
}
```

### 3. Get() API Signature (No Change)
The `db.Get()` method signature remains the same in v2:
```go
val, closer, err := db.Get(key)
// Still returns ([]byte, io.Closer, error)
```

## Files Modified

1. **go.mod** - Updated dependency versions
2. **fsm/store.go** - Updated import paths
3. **fsm/fsm.go** - Updated import paths
4. **fsm/apply.go** - Updated import paths
5. **fsm/wb.go** - Updated import paths
6. **fsm/utils.go** - Added `Errorf` method to Logger
7. **engine/raft.go** - Updated import paths
8. **openspec/project.md** - Updated version documentation

## Testing

- ✅ Build successful: `go build`
- ✅ Unit tests pass: `go test ./...`
- ✅ No runtime errors detected
- ✅ Backward compatibility maintained

## Performance

Performance testing requires a running Raft cluster. Expected improvements in v2.1.4:
- Better write amplification
- Improved iterator performance
- Enhanced stability for production workloads

## Migration Notes

### For Developers
- Update all Pebble imports to use `/v2` suffix
- Update all raft-pebbledb imports to use `/v2` suffix
- Add `Errorf` method to any custom Logger implementations
- Run `go mod tidy` after updating dependencies

### Data Compatibility
- Pebble v2 maintains backward compatibility with v1.1.2 on-disk format
- No data migration required
- Existing data files can be opened directly with v2.1.4

## Rollback Plan

If issues are encountered:

1. Revert import changes in all files
2. Update go.mod:
   ```
   github.com/cockroachdb/pebble v1.1.2
   github.com/xkeyideal/raft-pebbledb v1.7.0
   ```
3. Remove `Errorf` method from Logger (optional)
4. Run `go mod tidy`
5. Rebuild: `go build`

On-disk data remains compatible, so no data migration is needed for rollback.

## References

- [Pebble v2 Releases](https://github.com/cockroachdb/pebble/releases)
- [raft-pebbledb Releases](https://github.com/xkeyideal/raft-pebbledb/releases)
