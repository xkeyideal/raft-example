package fsm

import (
	"io"
	"sync"
	"time"

	"go.uber.org/atomic"

	pebble "github.com/cockroachdb/pebble/v2"
	"github.com/hashicorp/raft"
)

// SnapshotMetrics holds snapshot operation metrics
// This follows the Consul pattern for monitoring
type SnapshotMetrics struct {
	LastSnapshotDuration time.Duration
	LastSnapshotSize     int64
	LastSnapshotRecords  uint64
	LastSnapshotTime     time.Time
	LastRestoreDuration  time.Duration
	LastRestoreTime      time.Time
	SnapshotCount        uint64
	RestoreCount         uint64
}

type StateMachine struct {
	raftAddr string
	nodeId   string
	fsmIndex *atomic.Uint64
	store    *store

	// abandonCh is used to signal watchers that this state store has been
	// abandoned (usually during a restore). This is only ever closed.
	abandonCh   chan struct{}
	abandonOnce sync.Once

	// metrics holds snapshot operation metrics
	metrics     SnapshotMetrics
	metricsLock sync.RWMutex
}

func NewStateMachine(raftAddr string, nodeId string, dir string) (*StateMachine, error) {
	store, err := newStore(dir)
	if err != nil {
		return nil, err
	}

	return &StateMachine{
		raftAddr:  raftAddr,
		nodeId:    nodeId,
		store:     store,
		fsmIndex:  atomic.NewUint64(0),
		abandonCh: make(chan struct{}),
	}, nil
}

func (r *StateMachine) Stats() (map[string]any, error) {
	dirSz, err := DirSize(r.store.baseDir)
	if err != nil {
		return nil, err
	}

	r.metricsLock.RLock()
	metrics := r.metrics
	r.metricsLock.RUnlock()

	stats := map[string]any{
		"fsm_index":              r.GetFsmIndex(),
		"fsm_dir":                r.store.baseDir,
		"fsm_dir_size":           dirSz,
		"snapshot_count":         metrics.SnapshotCount,
		"restore_count":          metrics.RestoreCount,
		"last_snapshot_duration": metrics.LastSnapshotDuration.String(),
		"last_snapshot_size":     metrics.LastSnapshotSize,
		"last_snapshot_records":  metrics.LastSnapshotRecords,
		"last_snapshot_time":     metrics.LastSnapshotTime.Format(time.RFC3339),
		"last_restore_duration":  metrics.LastRestoreDuration.String(),
		"last_restore_time":      metrics.LastRestoreTime.Format(time.RFC3339),
	}

	return stats, nil
}

func (r *StateMachine) GetFsmIndex() uint64 {
	return r.fsmIndex.Load()
}

func (r *StateMachine) UpdateFsmIndex(index uint64) {
	if r.fsmIndex.Load() < index {
		r.fsmIndex.Store(index)
	}
}

func (r *StateMachine) ReadLocal(key []byte) *CommandResponse {
	val, err := r.store.query(key)
	return &CommandResponse{
		Val:   val,
		Error: err,
	}
}

// Apply is called once a log entry is committed by a majority of the cluster.
//
// Apply should apply the log to the FSM. Apply must be deterministic and
// produce the same result on all peers in the cluster.
//
// The returned value is returned to the client as the ApplyFuture.Response.
func (r *StateMachine) Apply(log *raft.Log) any {
	if r.store.isclosed() {
		return &CommandResponse{Error: pebble.ErrClosed}
	}

	r.fsmIndex.Store(log.Index)

	return r.store.applyCommand(log.Data)
}

// Snapshot returns an FSMSnapshot used to: support log compaction, to
// restore the FSM to a previous state, or to bring out-of-date followers up
// to a recent log index.
//
// The Snapshot implementation should return quickly, because Apply can not
// be called while Snapshot is running. Generally this means Snapshot should
// only capture a pointer to the state, and any expensive IO should happen
// as part of FSMSnapshot.Persist.
//
// Apply and Snapshot are always called from the same thread, but Apply will
// be called concurrently with FSMSnapshot.Persist. This means the FSM should
// be implemented to allow for concurrent updates while a snapshot is happening.
func (r *StateMachine) Snapshot() (raft.FSMSnapshot, error) {
	if r.store.isclosed() {
		return nil, pebble.ErrClosed
	}

	return &fsmSnapshot{
		fsm:       r,
		snapshot:  r.store.getSnapshot(),
		lastIndex: r.fsmIndex.Load(), // Capture current index for snapshot header
		startTime: time.Now(),        // Track snapshot start time for metrics
	}, nil
}

// Restore is used to restore an FSM from a snapshot. It is not called
// concurrently with any other command. The FSM must discard all previous
// state before restoring the snapshot.
func (r *StateMachine) Restore(reader io.ReadCloser) error {
	defer reader.Close() // Always close the reader when done

	if r.store.isclosed() {
		return pebble.ErrClosed
	}

	startTime := time.Now()

	header, err := r.store.recoverSnapShot(r.nodeId, r.raftAddr, reader)
	if err != nil {
		return err
	}

	// Restore fsmIndex from snapshot header
	// This ensures the FSM index is correctly restored after snapshot recovery
	if header != nil && header.LastIndex > 0 {
		r.fsmIndex.Store(header.LastIndex)
	}

	// Update metrics
	r.metricsLock.Lock()
	r.metrics.RestoreCount++
	r.metrics.LastRestoreDuration = time.Since(startTime)
	r.metrics.LastRestoreTime = time.Now()
	r.metricsLock.Unlock()

	// Abandon the old state - this signals any blocking queries to wake up
	// Following Consul's pattern: close abandonCh and create a new one
	r.abandon()

	return nil
}

// AbandonCh returns a channel you can wait on to know if the state store was
// abandoned (usually during a restore). Blocking queries should watch this
// to detect when they need to re-query after a restore.
func (r *StateMachine) AbandonCh() <-chan struct{} {
	return r.abandonCh
}

// abandon signals that the state store has been abandoned.
// This is called during Restore to notify blocking queries.
func (r *StateMachine) abandon() {
	// Close the current channel to signal watchers
	oldCh := r.abandonCh
	// Create a new channel for future watchers
	r.abandonCh = make(chan struct{})
	// Close the old channel to wake up any waiting goroutines
	close(oldCh)
}

func (r *StateMachine) Close() error {
	if r.store.isclosed() {
		return nil
	}

	return r.store.close()
}

type fsmSnapshot struct {
	fsm       *StateMachine
	snapshot  *pebble.Snapshot
	lastIndex uint64    // Last applied index at snapshot time
	startTime time.Time // Start time for metrics tracking
}

// Persist should dump all necessary state to the WriteCloser 'sink',
// and call sink.Close() when finished or call sink.Cancel() on error.
func (s *fsmSnapshot) Persist(sink raft.SnapshotSink) error {
	result, err := s.fsm.store.saveSnapShot(s.fsm.nodeId, s.fsm.raftAddr, s.lastIndex, s.snapshot, sink)
	if err != nil {
		return err
	}

	// Update metrics after successful persist
	s.fsm.metricsLock.Lock()
	s.fsm.metrics.SnapshotCount++
	s.fsm.metrics.LastSnapshotDuration = time.Since(s.startTime)
	s.fsm.metrics.LastSnapshotSize = result.Size
	s.fsm.metrics.LastSnapshotRecords = result.Records
	s.fsm.metrics.LastSnapshotTime = time.Now()
	s.fsm.metricsLock.Unlock()

	return nil
}

// Release is invoked when we are finished with the snapshot.
func (s *fsmSnapshot) Release() {
	s.snapshot.Close()
}
