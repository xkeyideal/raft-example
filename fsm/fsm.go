package fsm

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"

	"github.com/cockroachdb/pebble"
	"github.com/hashicorp/raft"
)

type StateMachine struct {
	raftAddr string
	nodeId   string
	store    *Store
}

func NewStateMachine(raftAddr string, nodeId string, s *Store) *StateMachine {
	return &StateMachine{
		raftAddr: raftAddr,
		nodeId:   nodeId,
		store:    s,
	}
}

type setPayload struct {
	Key   string
	Value string
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

	//  将raft的日志转换为db要执行的命令
	switch log.Type {
	case raft.LogCommand:
		var sp setPayload
		err := json.Unmarshal(log.Data, &sp)
		if err != nil {
			return fmt.Errorf("Could not parse payload: %s", err)
		}

		idx := log.Index
		idxByte := make([]byte, 8)
		binary.BigEndian.PutUint64(idxByte, idx)

		batch := r.store.batch()
		defer batch.Close()

		// 更新revision的值
		cf := r.store.getColumnFamily(cf_default)
		batch.Set(r.store.buildColumnFamilyKey(cf, []byte(sp.Key)), []byte(sp.Value), r.store.getWo())
		if err := r.store.write(batch); err != nil {
			return err
		}
	}

	return nil
}

func (r *StateMachine) Lookup(key []byte) ([]byte, error) {
	if r.store.isclosed() {
		return nil, pebble.ErrClosed
	}

	cf := r.store.getColumnFamily(cf_default)
	d, err := r.store.getBytes(r.store.buildColumnFamilyKey(cf, key))
	if err != nil {
		return nil, err
	}

	return d, nil
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
