// Package main provides integration tests for the Raft cluster.
// These tests require a running 3-node cluster at:
//   - localhost:40051
//   - localhost:40052
//   - localhost:40053
//
// Start the cluster before running tests:
//
//	go test -v ./cmd/...
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/xkeyideal/raft-example/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var clusterAddrs = []string{
	"localhost:40051",
	"localhost:40052",
	"localhost:40053",
}

// TestClient wraps a gRPC client connection for testing
type TestClient struct {
	conn   *grpc.ClientConn
	client pb.ExampleClient
	addr   string
}

func newTestClient(t *testing.T, addr string) *TestClient {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock())
	if err != nil {
		t.Fatalf("failed to connect to %s: %v", addr, err)
	}

	return &TestClient{
		conn:   conn,
		client: pb.NewExampleClient(conn),
		addr:   addr,
	}
}

func (c *TestClient) Close() {
	if c.conn != nil {
		c.conn.Close()
	}
}

// createClients creates clients to all cluster nodes
func createClients(t *testing.T) []*TestClient {
	clients := make([]*TestClient, len(clusterAddrs))
	for i, addr := range clusterAddrs {
		clients[i] = newTestClient(t, addr)
	}
	return clients
}

func closeClients(clients []*TestClient) {
	for _, c := range clients {
		c.Close()
	}
}

// ============================================================
// Test: Basic Write and Read
// ============================================================

func TestBasicWriteRead(t *testing.T) {
	client := newTestClient(t, clusterAddrs[0])
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Write a key-value pair
	key := fmt.Sprintf("test-key-%d", time.Now().UnixNano())
	val := "test-value-12345"

	addResp, err := client.client.Add(ctx, &pb.AddRequest{
		Key: key,
		Val: val,
	})
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	t.Logf("Written key=%s, commitIndex=%d", key, addResp.CommitIndex)

	if addResp.CommitIndex == 0 {
		t.Error("Expected non-zero commit index")
	}

	// Read back (stale read)
	getResp, err := client.client.Get(ctx, &pb.GetRequest{
		Key:          key,
		Linearizable: false,
	})
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if getResp.Value != val {
		t.Errorf("Expected value %q, got %q", val, getResp.Value)
	}

	t.Logf("Read key=%s, value=%s, readAtIndex=%d", key, getResp.Value, getResp.ReadAtIndex)
}

// ============================================================
// Test: Linearizable Read
// ============================================================

func TestLinearizableRead(t *testing.T) {
	client := newTestClient(t, clusterAddrs[0])
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Write a key-value pair
	key := fmt.Sprintf("linearizable-key-%d", time.Now().UnixNano())
	val := "linearizable-value"

	_, err := client.client.Add(ctx, &pb.AddRequest{
		Key: key,
		Val: val,
	})
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// Linearizable read (consistent)
	getResp, err := client.client.Get(ctx, &pb.GetRequest{
		Key:          key,
		Linearizable: true,
	})
	if err != nil {
		t.Fatalf("Linearizable Get failed: %v", err)
	}

	if getResp.Value != val {
		t.Errorf("Expected value %q, got %q", val, getResp.Value)
	}

	t.Logf("Linearizable read: key=%s, value=%s, readAtIndex=%d", key, getResp.Value, getResp.ReadAtIndex)
}

// ============================================================
// Test: Leader Forwarding (write to follower should succeed)
// ============================================================

func TestLeaderForwarding(t *testing.T) {
	clients := createClients(t)
	defer closeClients(clients)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Try to write through each node
	for i, client := range clients {
		key := fmt.Sprintf("forward-key-%d-%d", i, time.Now().UnixNano())
		val := fmt.Sprintf("forward-value-%d", i)

		addResp, err := client.client.Add(ctx, &pb.AddRequest{
			Key: key,
			Val: val,
		})
		if err != nil {
			t.Errorf("Add via node %d (%s) failed: %v", i, client.addr, err)
			continue
		}

		t.Logf("Write via node %d (%s): key=%s, commitIndex=%d",
			i, client.addr, key, addResp.CommitIndex)

		// Verify the write is readable from any node
		for j, readClient := range clients {
			getResp, err := readClient.client.Get(ctx, &pb.GetRequest{
				Key:          key,
				Linearizable: true,
			})
			if err != nil {
				t.Errorf("Get from node %d failed: %v", j, err)
				continue
			}
			if getResp.Value != val {
				t.Errorf("Node %d: expected %q, got %q", j, val, getResp.Value)
			}
		}
	}
}

// ============================================================
// Test: Concurrent Writes
// ============================================================

func TestConcurrentWrites(t *testing.T) {
	const numWriters = 10
	const writesPerWriter = 20

	clients := createClients(t)
	defer closeClients(clients)

	var wg sync.WaitGroup
	var successCount atomic.Int64
	var failCount atomic.Int64

	startTime := time.Now()

	for w := 0; w < numWriters; w++ {
		wg.Add(1)
		go func(writerID int) {
			defer wg.Done()

			client := clients[writerID%len(clients)]

			for i := 0; i < writesPerWriter; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

				key := fmt.Sprintf("concurrent-w%d-i%d-%d", writerID, i, time.Now().UnixNano())
				val := fmt.Sprintf("value-%d-%d", writerID, i)

				_, err := client.client.Add(ctx, &pb.AddRequest{
					Key: key,
					Val: val,
				})
				cancel()

				if err != nil {
					failCount.Add(1)
					t.Logf("Writer %d, iteration %d failed: %v", writerID, i, err)
				} else {
					successCount.Add(1)
				}
			}
		}(w)
	}

	wg.Wait()
	duration := time.Since(startTime)

	total := numWriters * writesPerWriter
	success := successCount.Load()
	fail := failCount.Load()

	t.Logf("Concurrent writes completed in %v", duration)
	t.Logf("Total: %d, Success: %d, Failed: %d", total, success, fail)
	t.Logf("Throughput: %.2f writes/sec", float64(success)/duration.Seconds())

	if fail > 0 {
		t.Logf("Warning: %d writes failed", fail)
	}

	// At least 90% should succeed
	successRate := float64(success) / float64(total)
	if successRate < 0.9 {
		t.Errorf("Success rate %.2f%% is below 90%%", successRate*100)
	}
}

// ============================================================
// Test: Concurrent Reads
// ============================================================

func TestConcurrentReads(t *testing.T) {
	client := newTestClient(t, clusterAddrs[0])
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// First write some data
	key := fmt.Sprintf("read-test-key-%d", time.Now().UnixNano())
	val := "read-test-value"

	_, err := client.client.Add(ctx, &pb.AddRequest{Key: key, Val: val})
	if err != nil {
		t.Fatalf("Setup write failed: %v", err)
	}

	// Wait for replication to all nodes before concurrent reads
	time.Sleep(500 * time.Millisecond)

	// Concurrent reads
	const numReaders = 50
	var wg sync.WaitGroup
	var successCount atomic.Int64

	clients := createClients(t)
	defer closeClients(clients)

	startTime := time.Now()

	for r := 0; r < numReaders; r++ {
		wg.Add(1)
		go func(readerID int) {
			defer wg.Done()

			c := clients[readerID%len(clients)]
			readCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			// Alternate between linearizable and stale reads
			linearizable := readerID%2 == 0

			resp, err := c.client.Get(readCtx, &pb.GetRequest{
				Key:          key,
				Linearizable: linearizable,
			})
			if err != nil {
				t.Logf("Reader %d failed: %v", readerID, err)
				return
			}

			if resp.Value != val {
				t.Logf("Reader %d got wrong value: %q (linearizable=%v)", readerID, resp.Value, linearizable)
				return
			}

			successCount.Add(1)
		}(r)
	}

	wg.Wait()
	duration := time.Since(startTime)

	success := successCount.Load()
	t.Logf("Concurrent reads: %d/%d succeeded in %v", success, numReaders, duration)
	t.Logf("Read throughput: %.2f reads/sec", float64(success)/duration.Seconds())

	if success < int64(numReaders*9/10) {
		t.Errorf("Too many read failures: only %d/%d succeeded", success, numReaders)
	}
}

// ============================================================
// Test: Cluster Stats
// ============================================================

func TestClusterStats(t *testing.T) {
	clients := createClients(t)
	defer closeClients(clients)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	leaderCount := 0
	var leaderAddr string

	for i, client := range clients {
		resp, err := client.client.Stat(ctx, &pb.StatRequest{})
		if err != nil {
			t.Errorf("Stat from node %d failed: %v", i, err)
			continue
		}

		// Parse stats JSON
		var stats map[string]interface{}
		if err := json.Unmarshal([]byte(resp.Stats), &stats); err != nil {
			t.Errorf("Failed to parse stats JSON from node %d: %v", i, err)
			continue
		}

		nodeID := stats["node_id"].(string)
		t.Logf("Node %d (%s): node_id=%s", i, client.addr, nodeID)

		// Check raft stats
		if raftStats, ok := stats["raft"].(map[string]interface{}); ok {
			state := raftStats["state"].(string)
			t.Logf("  State: %s", state)

			if state == "Leader" {
				leaderCount++
				leaderAddr = client.addr
			}

			if commitIndex, ok := raftStats["commit_index"]; ok {
				t.Logf("  Commit Index: %v", commitIndex)
			}
			if appliedIndex, ok := raftStats["applied_index"]; ok {
				t.Logf("  Applied Index: %v", appliedIndex)
			}
		}

		// Check FSM stats
		if fsmStats, ok := stats["fsm"].(map[string]interface{}); ok {
			if fsmIndex, ok := fsmStats["fsm_index"]; ok {
				t.Logf("  FSM Index: %v", fsmIndex)
			}
		}
	}

	// Verify exactly one leader
	if leaderCount != 1 {
		t.Errorf("Expected exactly 1 leader, found %d", leaderCount)
	} else {
		t.Logf("Leader is at: %s", leaderAddr)
	}
}

// ============================================================
// Test: Read Your Writes Consistency
// ============================================================

func TestReadYourWrites(t *testing.T) {
	client := newTestClient(t, clusterAddrs[0])
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Perform multiple write-then-read cycles
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("ryw-key-%d-%d", i, time.Now().UnixNano())
		val := fmt.Sprintf("ryw-value-%d", i)

		// Write
		addResp, err := client.client.Add(ctx, &pb.AddRequest{Key: key, Val: val})
		if err != nil {
			t.Fatalf("Write %d failed: %v", i, err)
		}

		// Immediately read (linearizable)
		getResp, err := client.client.Get(ctx, &pb.GetRequest{
			Key:          key,
			Linearizable: true,
		})
		if err != nil {
			t.Fatalf("Read %d failed: %v", i, err)
		}

		if getResp.Value != val {
			t.Errorf("Read-your-write failed for key %s: expected %q, got %q",
				key, val, getResp.Value)
		}

		// Verify read index >= commit index
		if getResp.ReadAtIndex < addResp.CommitIndex {
			t.Errorf("Read index %d < commit index %d",
				getResp.ReadAtIndex, addResp.CommitIndex)
		}
	}

	t.Log("Read-your-writes consistency verified for 10 operations")
}

// ============================================================
// Test: Key Not Found
// ============================================================

func TestKeyNotFound(t *testing.T) {
	client := newTestClient(t, clusterAddrs[0])
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Read a non-existent key
	key := fmt.Sprintf("nonexistent-key-%d", time.Now().UnixNano())

	resp, err := client.client.Get(ctx, &pb.GetRequest{
		Key:          key,
		Linearizable: false,
	})
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	// Should return empty value for non-existent key
	if resp.Value != "" {
		t.Errorf("Expected empty value for non-existent key, got %q", resp.Value)
	}

	t.Logf("Non-existent key correctly returns empty value")
}

// ============================================================
// Test: Overwrite Key
// ============================================================

func TestOverwriteKey(t *testing.T) {
	client := newTestClient(t, clusterAddrs[0])
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	key := fmt.Sprintf("overwrite-key-%d", time.Now().UnixNano())

	// Write initial value
	initialVal := "initial-value"
	_, err := client.client.Add(ctx, &pb.AddRequest{Key: key, Val: initialVal})
	if err != nil {
		t.Fatalf("Initial write failed: %v", err)
	}

	// Verify initial value
	resp, err := client.client.Get(ctx, &pb.GetRequest{Key: key, Linearizable: true})
	if err != nil {
		t.Fatalf("Get initial failed: %v", err)
	}
	if resp.Value != initialVal {
		t.Errorf("Expected %q, got %q", initialVal, resp.Value)
	}

	// Overwrite with new value
	newVal := "updated-value"
	_, err = client.client.Add(ctx, &pb.AddRequest{Key: key, Val: newVal})
	if err != nil {
		t.Fatalf("Overwrite failed: %v", err)
	}

	// Verify new value
	resp, err = client.client.Get(ctx, &pb.GetRequest{Key: key, Linearizable: true})
	if err != nil {
		t.Fatalf("Get updated failed: %v", err)
	}
	if resp.Value != newVal {
		t.Errorf("Expected %q, got %q", newVal, resp.Value)
	}

	t.Logf("Key overwrite verified: %s -> %s", initialVal, newVal)
}

// ============================================================
// Test: Large Value
// ============================================================

func TestLargeValue(t *testing.T) {
	client := newTestClient(t, clusterAddrs[0])
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	key := fmt.Sprintf("large-key-%d", time.Now().UnixNano())

	// Create a large value (1MB)
	largeVal := strings.Repeat("x", 1024*1024)

	_, err := client.client.Add(ctx, &pb.AddRequest{Key: key, Val: largeVal})
	if err != nil {
		t.Fatalf("Large value write failed: %v", err)
	}

	resp, err := client.client.Get(ctx, &pb.GetRequest{Key: key, Linearizable: true})
	if err != nil {
		t.Fatalf("Large value read failed: %v", err)
	}

	if len(resp.Value) != len(largeVal) {
		t.Errorf("Value length mismatch: expected %d, got %d", len(largeVal), len(resp.Value))
	}

	if resp.Value != largeVal {
		t.Error("Large value content mismatch")
	}

	t.Logf("Large value (1MB) write/read verified")
}

// ============================================================
// Test: Sequential Writes Ordering
// ============================================================

func TestSequentialWritesOrdering(t *testing.T) {
	client := newTestClient(t, clusterAddrs[0])
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Write multiple keys and track commit indices
	const numWrites = 20
	commitIndices := make([]uint64, numWrites)

	for i := 0; i < numWrites; i++ {
		key := fmt.Sprintf("order-key-%d-%d", i, time.Now().UnixNano())
		val := fmt.Sprintf("order-value-%d", i)

		resp, err := client.client.Add(ctx, &pb.AddRequest{Key: key, Val: val})
		if err != nil {
			t.Fatalf("Write %d failed: %v", i, err)
		}
		commitIndices[i] = resp.CommitIndex
	}

	// Verify commit indices are monotonically increasing
	for i := 1; i < numWrites; i++ {
		if commitIndices[i] <= commitIndices[i-1] {
			t.Errorf("Commit indices not monotonically increasing: index[%d]=%d, index[%d]=%d",
				i-1, commitIndices[i-1], i, commitIndices[i])
		}
	}

	t.Logf("Sequential writes ordering verified: %d writes with monotonic commit indices",
		numWrites)
	t.Logf("First commit index: %d, Last commit index: %d",
		commitIndices[0], commitIndices[numWrites-1])
}

// ============================================================
// Test: Cluster Replication (all nodes have same data)
// ============================================================

func TestClusterReplication(t *testing.T) {
	clients := createClients(t)
	defer closeClients(clients)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Write through node 0
	key := fmt.Sprintf("replication-key-%d", time.Now().UnixNano())
	val := "replication-value"

	_, err := clients[0].client.Add(ctx, &pb.AddRequest{Key: key, Val: val})
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	// Wait for replication
	time.Sleep(500 * time.Millisecond)

	// Verify all nodes have the data (using stale reads to check local state)
	for i, client := range clients {
		resp, err := client.client.Get(ctx, &pb.GetRequest{
			Key:          key,
			Linearizable: false, // Stale read to check local state
		})
		if err != nil {
			t.Errorf("Read from node %d failed: %v", i, err)
			continue
		}

		if resp.Value != val {
			t.Errorf("Node %d: expected %q, got %q", i, val, resp.Value)
		} else {
			t.Logf("Node %d (%s): value replicated correctly", i, client.addr)
		}
	}
}

// ============================================================
// Benchmark: Write Throughput
// ============================================================

func BenchmarkWrite(b *testing.B) {
	ctx := context.Background()

	conn, err := grpc.DialContext(ctx, clusterAddrs[0],
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		b.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close()

	client := pb.NewExampleClient(conn)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("bench-write-%d", i)
		val := fmt.Sprintf("bench-value-%d", i)

		_, err := client.Add(ctx, &pb.AddRequest{Key: key, Val: val})
		if err != nil {
			b.Fatalf("Add failed: %v", err)
		}
	}
}

// ============================================================
// Benchmark: Read Throughput (Stale)
// ============================================================

func BenchmarkReadStale(b *testing.B) {
	ctx := context.Background()

	conn, err := grpc.DialContext(ctx, clusterAddrs[0],
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		b.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close()

	client := pb.NewExampleClient(conn)

	// Setup: write a key first
	key := "bench-read-key"
	_, err = client.Add(ctx, &pb.AddRequest{Key: key, Val: "bench-value"})
	if err != nil {
		b.Fatalf("Setup write failed: %v", err)
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := client.Get(ctx, &pb.GetRequest{Key: key, Linearizable: false})
		if err != nil {
			b.Fatalf("Get failed: %v", err)
		}
	}
}

// ============================================================
// Benchmark: Read Throughput (Linearizable)
// ============================================================

func BenchmarkReadLinearizable(b *testing.B) {
	ctx := context.Background()

	conn, err := grpc.DialContext(ctx, clusterAddrs[0],
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		b.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close()

	client := pb.NewExampleClient(conn)

	// Setup: write a key first
	key := "bench-linearizable-key"
	_, err = client.Add(ctx, &pb.AddRequest{Key: key, Val: "bench-value"})
	if err != nil {
		b.Fatalf("Setup write failed: %v", err)
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := client.Get(ctx, &pb.GetRequest{Key: key, Linearizable: true})
		if err != nil {
			b.Fatalf("Get failed: %v", err)
		}
	}
}
