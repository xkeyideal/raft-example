// Package main provides an example of using the abandon mechanism for blocking queries.
//
// The abandon mechanism is a Consul-style pattern for handling state restoration
// during blocking/watch queries. When a snapshot restore happens, all blocking
// queries are immediately notified so they can re-establish their watches.
//
// This example demonstrates:
// 1. How to use AbandonCh() to detect state abandonment
// 2. How to implement blocking queries with abandon support
// 3. How to handle the abandoned state in client code
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/xkeyideal/raft-example/fsm"
	"github.com/xkeyideal/raft-example/service"
)

// Example: Using AbandonCh directly
func exampleDirectAbandonCh(stateMachine *fsm.StateMachine) {
	// Get the abandon channel
	abandonCh := stateMachine.AbandonCh()

	// In a goroutine, wait for abandon signal
	go func() {
		<-abandonCh
		log.Println("State was abandoned! Need to refresh all cached data.")
		// Re-query all data, reset caches, etc.
	}()
}

// Example: Using BlockingQuery for watch-style operations
func exampleBlockingQuery(ctx context.Context, stateMachine *fsm.StateMachine, kv service.KV) {
	var lastIndex uint64 = 0
	key := "my-watched-key"

	for {
		// Create query function
		queryFn := func() ([]byte, uint64, error) {
			index, val, err := kv.Query(ctx, []byte(key), false)
			return val, index, err
		}

		// Perform blocking query
		result, err := service.BlockingQuery(ctx, stateMachine, lastIndex+1, 30*time.Second, queryFn)
		if err != nil {
			log.Printf("Query error: %v", err)
			time.Sleep(time.Second)
			continue
		}

		if result.Abandoned {
			// State was restored from a snapshot
			// This is critical - we must reset our index and re-query
			log.Println("State abandoned due to restore, resetting watch...")
			lastIndex = 0
			continue
		}

		// Normal update
		log.Printf("Key '%s' updated at index %d: %s", key, result.CurrentIndex, string(result.Value))
		lastIndex = result.CurrentIndex
	}
}

// Example: Using WatchKey for continuous monitoring
func exampleWatchKey(ctx context.Context, stateMachine *fsm.StateMachine, kv service.KV) {
	key := "my-watched-key"

	queryFn := func() ([]byte, uint64, error) {
		index, val, err := kv.Query(ctx, []byte(key), false)
		return val, index, err
	}

	err := service.WatchKey(ctx, stateMachine, func(result *service.BlockingQueryResult) error {
		if result.Abandoned {
			// Handle abandon - perhaps refresh all local caches
			log.Println("State restored! Refreshing all cached data...")
			return refreshAllCaches()
		}

		// Handle normal update
		log.Printf("Received update at index %d", result.CurrentIndex)
		return processUpdate(result.Value)
	}, queryFn)

	if err != nil && err != context.Canceled {
		log.Printf("Watch ended with error: %v", err)
	}
}

// Example: Multiple watchers pattern
func exampleMultipleWatchers(stateMachine *fsm.StateMachine) {
	// Pattern for handling abandon in multiple concurrent watchers
	// Each watcher monitors the same abandonCh

	watcherCount := 3
	done := make(chan struct{})

	for i := 0; i < watcherCount; i++ {
		go func(id int) {
			for {
				select {
				case <-done:
					return
				case <-stateMachine.AbandonCh():
					// All watchers receive this signal simultaneously
					log.Printf("Watcher %d: state abandoned, refreshing...", id)
					// Each watcher handles the abandon independently
					refreshWatcherState(id)
				}
			}
		}(i)
	}

	// Cleanup
	// close(done)
}

// Placeholder functions for the examples
func refreshAllCaches() error {
	return nil
}

func processUpdate(value []byte) error {
	return nil
}

func refreshWatcherState(id int) {
	// Refresh state for this watcher
}

func main() {
	fmt.Println("This is an example file demonstrating the abandon mechanism.")
	fmt.Println("See the function comments for usage patterns.")
	fmt.Println()
	fmt.Println("Key points about the abandon mechanism:")
	fmt.Println("1. AbandonCh() returns a channel that closes when state is restored")
	fmt.Println("2. After abandon, a new channel is created for future watchers")
	fmt.Println("3. Clients should reset their minIndex and re-query after abandon")
	fmt.Println("4. This pattern prevents stale data issues after snapshot restore")
}
