package fsm

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/raft"
)

// TestNewStateMachine tests the StateMachine constructor
func TestNewStateMachine(t *testing.T) {
	dir := t.TempDir()

	fsm, err := NewStateMachine("127.0.0.1:9000", "node1", dir)
	if err != nil {
		t.Fatalf("failed to create StateMachine: %v", err)
	}
	defer fsm.Close()

	if fsm.raftAddr != "127.0.0.1:9000" {
		t.Errorf("expected raftAddr=127.0.0.1:9000, got %s", fsm.raftAddr)
	}

	if fsm.nodeId != "node1" {
		t.Errorf("expected nodeId=node1, got %s", fsm.nodeId)
	}

	if fsm.GetFsmIndex() != 0 {
		t.Errorf("expected initial fsmIndex=0, got %d", fsm.GetFsmIndex())
	}

	// Test AbandonCh is not nil
	if fsm.AbandonCh() == nil {
		t.Error("expected AbandonCh to be non-nil")
	}
}

// TestFsmIndexOperations tests GetFsmIndex and UpdateFsmIndex
func TestFsmIndexOperations(t *testing.T) {
	dir := t.TempDir()

	fsm, err := NewStateMachine("127.0.0.1:9000", "node1", dir)
	if err != nil {
		t.Fatalf("failed to create StateMachine: %v", err)
	}
	defer fsm.Close()

	// Initial index should be 0
	if fsm.GetFsmIndex() != 0 {
		t.Errorf("expected initial index=0, got %d", fsm.GetFsmIndex())
	}

	// Update to higher index
	fsm.UpdateFsmIndex(100)
	if fsm.GetFsmIndex() != 100 {
		t.Errorf("expected index=100, got %d", fsm.GetFsmIndex())
	}

	// Update to even higher index
	fsm.UpdateFsmIndex(200)
	if fsm.GetFsmIndex() != 200 {
		t.Errorf("expected index=200, got %d", fsm.GetFsmIndex())
	}

	// Try to update to lower index (should not change)
	fsm.UpdateFsmIndex(50)
	if fsm.GetFsmIndex() != 200 {
		t.Errorf("expected index to remain 200, got %d", fsm.GetFsmIndex())
	}
}

// TestApplyInsertAndQuery tests the Apply method for insert and query operations
func TestApplyInsertAndQuery(t *testing.T) {
	dir := t.TempDir()

	fsm, err := NewStateMachine("127.0.0.1:9000", "node1", dir)
	if err != nil {
		t.Fatalf("failed to create StateMachine: %v", err)
	}
	defer fsm.Close()

	// Create insert command
	payload := SetPayload{
		Key:   []byte("test-key"),
		Value: []byte("test-value"),
	}
	payloadBytes, _ := json.Marshal(payload)

	cmd := Command{
		Type:    Insert,
		Payload: payloadBytes,
	}
	cmdBytes, _ := json.Marshal(cmd)

	// Apply insert command
	log := &raft.Log{
		Index: 1,
		Data:  cmdBytes,
	}
	result := fsm.Apply(log)
	resp, ok := result.(*CommandResponse)
	if !ok {
		t.Fatalf("expected *CommandResponse, got %T", result)
	}
	if resp.Error != nil {
		t.Fatalf("insert failed: %v", resp.Error)
	}

	// Verify fsmIndex updated
	if fsm.GetFsmIndex() != 1 {
		t.Errorf("expected fsmIndex=1, got %d", fsm.GetFsmIndex())
	}

	// Query the inserted value using ReadLocal
	queryResult := fsm.ReadLocal([]byte("test-key"))
	if queryResult.Error != nil {
		t.Fatalf("query failed: %v", queryResult.Error)
	}
	if !bytes.Equal(queryResult.Val, []byte("test-value")) {
		t.Errorf("expected value='test-value', got '%s'", queryResult.Val)
	}
}

// TestApplyMultipleInserts tests multiple insert operations
func TestApplyMultipleInserts(t *testing.T) {
	dir := t.TempDir()

	fsm, err := NewStateMachine("127.0.0.1:9000", "node1", dir)
	if err != nil {
		t.Fatalf("failed to create StateMachine: %v", err)
	}
	defer fsm.Close()

	testData := map[string]string{
		"key1": "value1",
		"key2": "value2",
		"key3": "value3",
	}

	// Insert all test data
	var index uint64 = 1
	for key, val := range testData {
		payload := SetPayload{
			Key:   []byte(key),
			Value: []byte(val),
		}
		payloadBytes, _ := json.Marshal(payload)

		cmd := Command{
			Type:    Insert,
			Payload: payloadBytes,
		}
		cmdBytes, _ := json.Marshal(cmd)

		log := &raft.Log{
			Index: index,
			Data:  cmdBytes,
		}
		result := fsm.Apply(log)
		resp := result.(*CommandResponse)
		if resp.Error != nil {
			t.Fatalf("insert key=%s failed: %v", key, resp.Error)
		}
		index++
	}

	// Verify all data can be queried
	for key, expectedVal := range testData {
		result := fsm.ReadLocal([]byte(key))
		if result.Error != nil {
			t.Fatalf("query key=%s failed: %v", key, result.Error)
		}
		if !bytes.Equal(result.Val, []byte(expectedVal)) {
			t.Errorf("key=%s: expected value='%s', got '%s'", key, expectedVal, result.Val)
		}
	}
}

// TestApplyInvalidCommand tests Apply with invalid command data
func TestApplyInvalidCommand(t *testing.T) {
	dir := t.TempDir()

	fsm, err := NewStateMachine("127.0.0.1:9000", "node1", dir)
	if err != nil {
		t.Fatalf("failed to create StateMachine: %v", err)
	}
	defer fsm.Close()

	// Apply invalid JSON
	log := &raft.Log{
		Index: 1,
		Data:  []byte("invalid json"),
	}
	result := fsm.Apply(log)
	resp := result.(*CommandResponse)
	if resp.Error == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

// TestReadLocalNonexistentKey tests reading a key that doesn't exist
func TestReadLocalNonexistentKey(t *testing.T) {
	dir := t.TempDir()

	fsm, err := NewStateMachine("127.0.0.1:9000", "node1", dir)
	if err != nil {
		t.Fatalf("failed to create StateMachine: %v", err)
	}
	defer fsm.Close()

	result := fsm.ReadLocal([]byte("nonexistent-key"))
	if result.Error != nil {
		t.Fatalf("expected no error, got: %v", result.Error)
	}
	if len(result.Val) != 0 {
		t.Errorf("expected empty value, got: %s", result.Val)
	}
}

// TestStats tests the Stats method
func TestStats(t *testing.T) {
	dir := t.TempDir()

	fsm, err := NewStateMachine("127.0.0.1:9000", "node1", dir)
	if err != nil {
		t.Fatalf("failed to create StateMachine: %v", err)
	}
	defer fsm.Close()

	stats, err := fsm.Stats()
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}

	// Verify required stats fields exist
	requiredFields := []string{
		"fsm_index",
		"fsm_dir",
		"fsm_dir_size",
		"snapshot_count",
		"restore_count",
	}

	for _, field := range requiredFields {
		if _, ok := stats[field]; !ok {
			t.Errorf("missing required field: %s", field)
		}
	}
}

// TestAbandonChannel tests the abandon mechanism
func TestAbandonChannel(t *testing.T) {
	dir := t.TempDir()

	fsm, err := NewStateMachine("127.0.0.1:9000", "node1", dir)
	if err != nil {
		t.Fatalf("failed to create StateMachine: %v", err)
	}
	defer fsm.Close()

	oldCh := fsm.AbandonCh()

	// Use goroutine to wait for abandon
	var wg sync.WaitGroup
	wg.Add(1)
	abandoned := false

	go func() {
		defer wg.Done()
		select {
		case <-oldCh:
			abandoned = true
		case <-time.After(time.Second):
			// Timeout
		}
	}()

	// Trigger abandon
	fsm.abandon()

	wg.Wait()

	if !abandoned {
		t.Error("expected abandon channel to be closed")
	}

	// Verify new channel is created
	newCh := fsm.AbandonCh()
	if newCh == oldCh {
		t.Error("expected new abandon channel after abandon()")
	}

	// Verify new channel is still open
	select {
	case <-newCh:
		t.Error("new channel should not be closed yet")
	default:
		// Expected: channel is still open
	}
}

// mockSnapshotSink implements raft.SnapshotSink for testing
type mockSnapshotSink struct {
	buf      bytes.Buffer
	closed   bool
	canceled bool
}

func (m *mockSnapshotSink) Write(p []byte) (n int, err error) {
	return m.buf.Write(p)
}

func (m *mockSnapshotSink) Close() error {
	m.closed = true
	return nil
}

func (m *mockSnapshotSink) Cancel() error {
	m.canceled = true
	return nil
}

func (m *mockSnapshotSink) ID() string {
	return "test-snapshot-id"
}

// TestSnapshot tests the Snapshot method
func TestSnapshot(t *testing.T) {
	dir := t.TempDir()

	fsm, err := NewStateMachine("127.0.0.1:9000", "node1", dir)
	if err != nil {
		t.Fatalf("failed to create StateMachine: %v", err)
	}
	defer fsm.Close()

	// Insert some test data
	testData := map[string]string{
		"key1": "value1",
		"key2": "value2",
		"key3": "value3",
	}

	var index uint64 = 1
	for key, val := range testData {
		payload := SetPayload{Key: []byte(key), Value: []byte(val)}
		payloadBytes, _ := json.Marshal(payload)
		cmd := Command{Type: Insert, Payload: payloadBytes}
		cmdBytes, _ := json.Marshal(cmd)
		fsm.Apply(&raft.Log{Index: index, Data: cmdBytes})
		index++
	}

	// Create snapshot
	snapshot, err := fsm.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}
	defer snapshot.Release()

	// Persist snapshot
	sink := &mockSnapshotSink{}
	err = snapshot.Persist(sink)
	if err != nil {
		t.Fatalf("Persist failed: %v", err)
	}

	if !sink.closed {
		t.Error("expected sink to be closed after Persist")
	}

	if sink.buf.Len() == 0 {
		t.Error("expected snapshot data, got empty buffer")
	}

	// Verify snapshot header
	data := sink.buf.Bytes()
	if len(data) < 16 {
		t.Fatal("snapshot data too short for header")
	}

	magic := binary.LittleEndian.Uint32(data[0:4])
	if magic != snapshotMagic {
		t.Errorf("expected magic=0x%08x, got 0x%08x", snapshotMagic, magic)
	}

	version := binary.LittleEndian.Uint32(data[4:8])
	if version != snapshotVersion {
		t.Errorf("expected version=%d, got %d", snapshotVersion, version)
	}
}

// TestSnapshotAndRestore tests the full snapshot/restore cycle
func TestSnapshotAndRestore(t *testing.T) {
	dir1 := t.TempDir()

	fsm1, err := NewStateMachine("127.0.0.1:9000", "node1", dir1)
	if err != nil {
		t.Fatalf("failed to create StateMachine 1: %v", err)
	}
	defer fsm1.Close()

	// Insert test data
	testData := map[string]string{
		"key1": "value1",
		"key2": "value2",
		"key3": "value3",
	}

	var index uint64 = 1
	for key, val := range testData {
		payload := SetPayload{Key: []byte(key), Value: []byte(val)}
		payloadBytes, _ := json.Marshal(payload)
		cmd := Command{Type: Insert, Payload: payloadBytes}
		cmdBytes, _ := json.Marshal(cmd)
		fsm1.Apply(&raft.Log{Index: index, Data: cmdBytes})
		index++
	}

	// Create and persist snapshot
	snapshot, err := fsm1.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	sink := &mockSnapshotSink{}
	err = snapshot.Persist(sink)
	if err != nil {
		t.Fatalf("Persist failed: %v", err)
	}
	snapshot.Release()

	snapshotData := sink.buf.Bytes()
	originalFsmIndex := fsm1.GetFsmIndex()

	// Insert more data after snapshot
	payload := SetPayload{Key: []byte("after-snapshot"), Value: []byte("new-value")}
	payloadBytes, _ := json.Marshal(payload)
	cmd := Command{Type: Insert, Payload: payloadBytes}
	cmdBytes, _ := json.Marshal(cmd)
	fsm1.Apply(&raft.Log{Index: 100, Data: cmdBytes})

	// Verify the new data exists
	result := fsm1.ReadLocal([]byte("after-snapshot"))
	if result.Error != nil || !bytes.Equal(result.Val, []byte("new-value")) {
		t.Fatal("after-snapshot key should exist before restore")
	}

	// Restore from snapshot (on same FSM - simulates follower receiving snapshot)
	reader := io.NopCloser(bytes.NewReader(snapshotData))
	err = fsm1.Restore(reader)
	if err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	// Verify fsmIndex is restored to snapshot time
	if fsm1.GetFsmIndex() != originalFsmIndex {
		t.Errorf("fsmIndex mismatch: expected %d, got %d", originalFsmIndex, fsm1.GetFsmIndex())
	}

	// Verify all snapshot data is restored
	for key, expectedVal := range testData {
		result := fsm1.ReadLocal([]byte(key))
		if result.Error != nil {
			t.Fatalf("query key=%s failed after restore: %v", key, result.Error)
		}
		if !bytes.Equal(result.Val, []byte(expectedVal)) {
			t.Errorf("key=%s: expected value='%s', got '%s'", key, expectedVal, result.Val)
		}
	}

	// Verify data added after snapshot is gone (DB was replaced)
	result = fsm1.ReadLocal([]byte("after-snapshot"))
	if len(result.Val) > 0 {
		t.Error("after-snapshot key should not exist after restore")
	}

	// Verify restore metrics updated
	stats, _ := fsm1.Stats()
	if stats["restore_count"].(uint64) < 1 {
		t.Errorf("expected restore_count >= 1, got %v", stats["restore_count"])
	}
}

// TestRestoreTriggersAbandon tests that Restore triggers the abandon channel
func TestRestoreTriggersAbandon(t *testing.T) {
	dir1 := t.TempDir()

	fsm1, err := NewStateMachine("127.0.0.1:9000", "node1", dir1)
	if err != nil {
		t.Fatalf("failed to create StateMachine 1: %v", err)
	}
	defer fsm1.Close()

	// Insert some data and create snapshot
	payload := SetPayload{Key: []byte("key"), Value: []byte("value")}
	payloadBytes, _ := json.Marshal(payload)
	cmd := Command{Type: Insert, Payload: payloadBytes}
	cmdBytes, _ := json.Marshal(cmd)
	fsm1.Apply(&raft.Log{Index: 1, Data: cmdBytes})

	snapshot, _ := fsm1.Snapshot()
	sink := &mockSnapshotSink{}
	snapshot.Persist(sink)
	snapshot.Release()

	snapshotData := sink.buf.Bytes()

	// Get abandon channel before restore
	abandonCh := fsm1.AbandonCh()

	// Setup watcher
	var wg sync.WaitGroup
	wg.Add(1)
	abandoned := false

	go func() {
		defer wg.Done()
		select {
		case <-abandonCh:
			abandoned = true
		case <-time.After(2 * time.Second):
		}
	}()

	// Restore on same FSM
	reader := io.NopCloser(bytes.NewReader(snapshotData))
	err = fsm1.Restore(reader)
	if err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	wg.Wait()

	if !abandoned {
		t.Error("expected abandon channel to be closed after Restore")
	}

	// Verify new abandon channel is created
	newAbandonCh := fsm1.AbandonCh()
	if newAbandonCh == abandonCh {
		t.Error("expected new abandon channel after Restore")
	}
}

// TestCloseStateMachine tests closing the state machine
func TestCloseStateMachine(t *testing.T) {
	dir := t.TempDir()

	fsm, err := NewStateMachine("127.0.0.1:9000", "node1", dir)
	if err != nil {
		t.Fatalf("failed to create StateMachine: %v", err)
	}

	// Close should not error
	err = fsm.Close()
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Double close should not error
	err = fsm.Close()
	if err != nil {
		t.Fatalf("Second Close failed: %v", err)
	}
}

// TestApplyAfterClose tests that Apply returns error after close
func TestApplyAfterClose(t *testing.T) {
	dir := t.TempDir()

	fsm, err := NewStateMachine("127.0.0.1:9000", "node1", dir)
	if err != nil {
		t.Fatalf("failed to create StateMachine: %v", err)
	}

	// Close the FSM
	fsm.Close()

	// Apply should return error
	payload := SetPayload{Key: []byte("key"), Value: []byte("value")}
	payloadBytes, _ := json.Marshal(payload)
	cmd := Command{Type: Insert, Payload: payloadBytes}
	cmdBytes, _ := json.Marshal(cmd)

	result := fsm.Apply(&raft.Log{Index: 1, Data: cmdBytes})
	resp := result.(*CommandResponse)
	if resp.Error == nil {
		t.Error("expected error when applying after close")
	}
}

// TestSnapshotMetrics tests that snapshot metrics are properly updated
func TestSnapshotMetrics(t *testing.T) {
	dir := t.TempDir()

	fsm, err := NewStateMachine("127.0.0.1:9000", "node1", dir)
	if err != nil {
		t.Fatalf("failed to create StateMachine: %v", err)
	}
	defer fsm.Close()

	// Insert data
	payload := SetPayload{Key: []byte("key"), Value: []byte("value")}
	payloadBytes, _ := json.Marshal(payload)
	cmd := Command{Type: Insert, Payload: payloadBytes}
	cmdBytes, _ := json.Marshal(cmd)
	fsm.Apply(&raft.Log{Index: 1, Data: cmdBytes})

	// Create and persist snapshot
	snapshot, err := fsm.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	sink := &mockSnapshotSink{}
	err = snapshot.Persist(sink)
	if err != nil {
		t.Fatalf("Persist failed: %v", err)
	}
	snapshot.Release()

	// Check metrics
	stats, _ := fsm.Stats()

	if stats["snapshot_count"].(uint64) != 1 {
		t.Errorf("expected snapshot_count=1, got %v", stats["snapshot_count"])
	}

	if stats["last_snapshot_size"].(int64) <= 0 {
		t.Errorf("expected positive snapshot size, got %v", stats["last_snapshot_size"])
	}

	if stats["last_snapshot_records"].(uint64) == 0 {
		t.Error("expected at least 1 snapshot record")
	}
}

// TestConcurrentApply tests concurrent Apply operations
func TestConcurrentApply(t *testing.T) {
	dir := t.TempDir()

	fsm, err := NewStateMachine("127.0.0.1:9000", "node1", dir)
	if err != nil {
		t.Fatalf("failed to create StateMachine: %v", err)
	}
	defer fsm.Close()

	const numGoroutines = 10
	const numOperations = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for g := 0; g < numGoroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < numOperations; i++ {
				key := []byte(filepath.Join("goroutine", string(rune(gid)), string(rune(i))))
				val := []byte("value")

				payload := SetPayload{Key: key, Value: val}
				payloadBytes, _ := json.Marshal(payload)
				cmd := Command{Type: Insert, Payload: payloadBytes}
				cmdBytes, _ := json.Marshal(cmd)

				index := uint64(gid*numOperations + i + 1)
				result := fsm.Apply(&raft.Log{Index: index, Data: cmdBytes})
				resp := result.(*CommandResponse)
				if resp.Error != nil {
					t.Errorf("goroutine %d, op %d failed: %v", gid, i, resp.Error)
				}
			}
		}(g)
	}

	wg.Wait()
}

// TestLargeValueSnapshot tests snapshot with large values
func TestLargeValueSnapshot(t *testing.T) {
	dir1 := t.TempDir()

	fsm1, err := NewStateMachine("127.0.0.1:9000", "node1", dir1)
	if err != nil {
		t.Fatalf("failed to create StateMachine: %v", err)
	}
	defer fsm1.Close()

	// Insert large value (1MB)
	largeValue := make([]byte, 1024*1024)
	for i := range largeValue {
		largeValue[i] = byte(i % 256)
	}

	payload := SetPayload{Key: []byte("large-key"), Value: largeValue}
	payloadBytes, _ := json.Marshal(payload)
	cmd := Command{Type: Insert, Payload: payloadBytes}
	cmdBytes, _ := json.Marshal(cmd)
	fsm1.Apply(&raft.Log{Index: 1, Data: cmdBytes})

	// Create and persist snapshot
	snapshot, err := fsm1.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	sink := &mockSnapshotSink{}
	err = snapshot.Persist(sink)
	if err != nil {
		t.Fatalf("Persist failed: %v", err)
	}
	snapshot.Release()

	snapshotData := sink.buf.Bytes()

	// Restore on same FSM
	reader := io.NopCloser(bytes.NewReader(snapshotData))
	err = fsm1.Restore(reader)
	if err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	// Verify large value is correctly restored
	result := fsm1.ReadLocal([]byte("large-key"))
	if result.Error != nil {
		t.Fatalf("query large-key failed: %v", result.Error)
	}
	if !bytes.Equal(result.Val, largeValue) {
		t.Error("large value mismatch after restore")
	}
}

// BenchmarkApply benchmarks the Apply method
func BenchmarkApply(b *testing.B) {
	dir := b.TempDir()

	fsm, err := NewStateMachine("127.0.0.1:9000", "node1", dir)
	if err != nil {
		b.Fatalf("failed to create StateMachine: %v", err)
	}
	defer fsm.Close()

	payload := SetPayload{Key: []byte("bench-key"), Value: []byte("bench-value")}
	payloadBytes, _ := json.Marshal(payload)
	cmd := Command{Type: Insert, Payload: payloadBytes}
	cmdBytes, _ := json.Marshal(cmd)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fsm.Apply(&raft.Log{Index: uint64(i + 1), Data: cmdBytes})
	}
}

// BenchmarkReadLocal benchmarks the ReadLocal method
func BenchmarkReadLocal(b *testing.B) {
	dir := b.TempDir()

	fsm, err := NewStateMachine("127.0.0.1:9000", "node1", dir)
	if err != nil {
		b.Fatalf("failed to create StateMachine: %v", err)
	}
	defer fsm.Close()

	// Insert test data first
	payload := SetPayload{Key: []byte("bench-key"), Value: []byte("bench-value")}
	payloadBytes, _ := json.Marshal(payload)
	cmd := Command{Type: Insert, Payload: payloadBytes}
	cmdBytes, _ := json.Marshal(cmd)
	fsm.Apply(&raft.Log{Index: 1, Data: cmdBytes})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fsm.ReadLocal([]byte("bench-key"))
	}
}

// BenchmarkSnapshot benchmarks the Snapshot creation and persist
func BenchmarkSnapshot(b *testing.B) {
	dir := b.TempDir()

	fsm, err := NewStateMachine("127.0.0.1:9000", "node1", dir)
	if err != nil {
		b.Fatalf("failed to create StateMachine: %v", err)
	}
	defer fsm.Close()

	// Insert 1000 keys
	for i := 0; i < 1000; i++ {
		payload := SetPayload{
			Key:   []byte("key-" + string(rune(i))),
			Value: []byte("value-" + string(rune(i))),
		}
		payloadBytes, _ := json.Marshal(payload)
		cmd := Command{Type: Insert, Payload: payloadBytes}
		cmdBytes, _ := json.Marshal(cmd)
		fsm.Apply(&raft.Log{Index: uint64(i + 1), Data: cmdBytes})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		snapshot, _ := fsm.Snapshot()
		sink := &mockSnapshotSink{}
		snapshot.Persist(sink)
		snapshot.Release()
	}
}
