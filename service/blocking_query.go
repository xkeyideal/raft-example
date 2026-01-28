package service

import (
	"context"
	"errors"
	"time"

	"github.com/xkeyideal/raft-example/fsm"
)

// BlockingQueryTimeout is the default timeout for blocking queries
const BlockingQueryTimeout = 5 * time.Minute

// ErrStateAbandoned is returned when the state store was abandoned during a blocking query
// This typically happens during a snapshot restore. Clients should re-issue their query.
var ErrStateAbandoned = errors.New("state abandoned due to restore, please retry")

// BlockingQueryResult represents the result of a blocking query
type BlockingQueryResult struct {
	Value        []byte
	CurrentIndex uint64
	Abandoned    bool // True if the query was interrupted due to state abandonment
}

// BlockingQuery demonstrates the use of FSM's abandon mechanism for blocking queries.
// This pattern is used in Consul for watch-style operations where a client wants to
// be notified when data changes.
//
// The abandon mechanism ensures that if a snapshot restore happens while a client
// is waiting, the client is promptly notified and can re-establish their query
// with the new state.
//
// Parameters:
//   - ctx: Context for cancellation
//   - fsm: The state machine that provides AbandonCh
//   - key: The key to watch
//   - minIndex: Minimum index to wait for (0 means return immediately with current value)
//   - timeout: Maximum time to wait before returning (0 means use default)
//   - queryFn: Function that performs the actual query and returns (value, currentIndex, error)
//
// Returns:
//   - BlockingQueryResult containing the value, current index, and whether state was abandoned
//   - error if the query failed
//
// Example usage:
//
//	result, err := BlockingQuery(ctx, fsm, "my-key", lastSeenIndex, 30*time.Second,
//	    func() ([]byte, uint64, error) {
//	        return kv.GetValue("my-key")
//	    })
//	if err != nil {
//	    return err
//	}
//	if result.Abandoned {
//	    // State was restored, retry the query from scratch
//	    return retryFromBeginning()
//	}
//	// Process result.Value at result.CurrentIndex
func BlockingQuery(
	ctx context.Context,
	stateMachine *fsm.StateMachine,
	minIndex uint64,
	timeout time.Duration,
	queryFn func() ([]byte, uint64, error),
) (*BlockingQueryResult, error) {
	if timeout == 0 {
		timeout = BlockingQueryTimeout
	}

	// Create a timeout context
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// First, perform the initial query
	value, currentIndex, err := queryFn()
	if err != nil {
		return nil, err
	}

	// If the current index is already >= minIndex, return immediately
	// This handles the case where data has already changed
	if minIndex == 0 || currentIndex >= minIndex {
		return &BlockingQueryResult{
			Value:        value,
			CurrentIndex: currentIndex,
			Abandoned:    false,
		}, nil
	}

	// Otherwise, we need to wait for changes
	// We use a ticker to periodically re-check, combined with the abandon channel
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeoutCtx.Done():
			// Timeout or context cancelled, return current value
			return &BlockingQueryResult{
				Value:        value,
				CurrentIndex: currentIndex,
				Abandoned:    false,
			}, nil

		case <-stateMachine.AbandonCh():
			// State was abandoned (typically due to snapshot restore)
			// This is the KEY FEATURE of the abandon mechanism!
			// The client should be notified immediately so they can:
			// 1. Re-query with the new state
			// 2. Reset their minIndex to 0
			// 3. Re-establish their watch
			return &BlockingQueryResult{
				Value:        nil,
				CurrentIndex: 0,
				Abandoned:    true,
			}, nil

		case <-ticker.C:
			// Periodically re-check if the index has advanced
			value, currentIndex, err = queryFn()
			if err != nil {
				return nil, err
			}

			if currentIndex >= minIndex {
				return &BlockingQueryResult{
					Value:        value,
					CurrentIndex: currentIndex,
					Abandoned:    false,
				}, nil
			}
		}
	}
}

// WatchKey is a higher-level API that continuously watches a key for changes.
// It uses the BlockingQuery pattern internally and handles abandon/retry automatically.
//
// The callback is invoked whenever:
// 1. The key's value changes
// 2. The state is restored (with abandoned=true)
//
// This demonstrates a complete watch implementation using the abandon mechanism.
//
// Example usage:
//
//	ctx, cancel := context.WithCancel(context.Background())
//	defer cancel()
//
//	err := WatchKey(ctx, fsm, "my-key", func(result *BlockingQueryResult) error {
//	    if result.Abandoned {
//	        log.Println("State was restored, refreshing all data...")
//	        return refreshAllData()
//	    }
//	    log.Printf("Key updated at index %d: %s", result.CurrentIndex, result.Value)
//	    return nil
//	}, queryFn)
func WatchKey(
	ctx context.Context,
	stateMachine *fsm.StateMachine,
	callback func(result *BlockingQueryResult) error,
	queryFn func() ([]byte, uint64, error),
) error {
	var lastIndex uint64 = 0

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		result, err := BlockingQuery(ctx, stateMachine, lastIndex+1, 30*time.Second, queryFn)
		if err != nil {
			return err
		}

		// Invoke callback
		if err := callback(result); err != nil {
			return err
		}

		if result.Abandoned {
			// Reset lastIndex after abandon - start fresh with new state
			lastIndex = 0
		} else {
			// Update lastIndex for next iteration
			lastIndex = result.CurrentIndex
		}
	}
}
