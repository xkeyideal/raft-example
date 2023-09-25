package fsm

import (
	"io"

	"go.uber.org/atomic"

	"github.com/cockroachdb/pebble"
	"github.com/hashicorp/raft"
)

type StateMachine struct {
	raftAddr string
	nodeId   string
	fsmIndex *atomic.Uint64
	store    *Store
}

func NewStateMachine(raftAddr string, nodeId string, s *Store) *StateMachine {
	return &StateMachine{
		raftAddr: raftAddr,
		nodeId:   nodeId,
		store:    s,
		fsmIndex: atomic.NewUint64(0),
	}
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
		return nil
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
		fsm:      r,
		snapshot: r.store.getSnapshot(),
	}, nil
}

// Restore is used to restore an FSM from a snapshot. It is not called
// concurrently with any other command. The FSM must discard all previous
// state before restoring the snapshot.
func (r *StateMachine) Restore(reader io.ReadCloser) error {
	if r.store.isclosed() {
		return pebble.ErrClosed
	}

	return r.store.recoverSnapShot(r.nodeId, r.raftAddr, reader)
}

func (r *StateMachine) Close() error {
	if r.store.isclosed() {
		return nil
	}

	return r.store.close()
}

type fsmSnapshot struct {
	fsm      *StateMachine
	snapshot *pebble.Snapshot
}

// Persist should dump all necessary state to the WriteCloser 'sink',
// and call sink.Close() when finished or call sink.Cancel() on error.
func (s *fsmSnapshot) Persist(sink raft.SnapshotSink) error {
	return s.fsm.store.saveSnapShot(s.fsm.nodeId, s.fsm.raftAddr, s.snapshot, sink)
}

// Release is invoked when we are finished with the snapshot.
func (s *fsmSnapshot) Release() {
	s.snapshot.Close()
}
