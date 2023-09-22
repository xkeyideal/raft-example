package engine

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/raft"

	"github.com/xkeyideal/raft-example/fsm"
	pebbledb "github.com/xkeyideal/raft-pebbledb"
)

const (
	barrierWriteTimeout = 2 * time.Minute
)

type raftServer struct {
	store  *fsm.StateMachine
	raft   *raft.Raft
	raftdb *pebbledb.PebbleStore

	// raftNotifyCh is set up by setupRaft() and ensures that we get reliable leader
	// transition notifications from the Raft layer.
	raftNotifyCh <-chan bool

	// readyForConsistentReads is used to track when the leader server is
	// ready to serve consistent reads, after it has applied its initial
	// barrier. This is updated atomically.
	readyForConsistentReads int32

	shutdownCh chan struct{}
}

func newRaft(baseDir, nodeId, raftAddr string, rfsm *fsm.StateMachine, raftBootstrap bool) (*raftServer, error) {
	s := &raftServer{
		store:        rfsm,
		raftNotifyCh: make(<-chan bool, 10),
		shutdownCh:   make(chan struct{}),
	}

	cfg := &raft.Config{
		ProtocolVersion:    raft.ProtocolVersionMax,
		HeartbeatTimeout:   1000 * time.Millisecond,
		ElectionTimeout:    1000 * time.Millisecond,
		CommitTimeout:      50 * time.Millisecond,
		MaxAppendEntries:   64,
		ShutdownOnRemove:   true,
		TrailingLogs:       10240,
		SnapshotInterval:   120 * time.Second,
		SnapshotThreshold:  8192,
		LeaderLeaseTimeout: 500 * time.Millisecond,
		LogLevel:           hclog.Warn.String(),
		LocalID:            raft.ServerID(nodeId),
	}

	raftdb, err := pebbledb.NewPebbleStore(filepath.Join(baseDir, "raftdb"), &fsm.Logger{}, pebbledb.DefaultPebbleDBConfig())
	if err != nil {
		return nil, fmt.Errorf(`pebbledb.NewPebbleStore(%q): %v`, filepath.Join(baseDir, "raftdb"), err)
	}

	s.raftdb = raftdb

	snapshotDir := filepath.Join(baseDir, "raft-snapshot")
	fss, err := raft.NewFileSnapshotStore(snapshotDir, 3, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf(`raft.NewFileSnapshotStore(%q, ...): %v`, snapshotDir, err)
	}

	tcpAddr, err := net.ResolveTCPAddr("tcp", raftAddr)
	if err != nil {
		return nil, fmt.Errorf("Could not resolve address: %s", err)
	}

	transport, err := raft.NewTCPTransport(raftAddr, tcpAddr, 10, time.Second*10, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("Could not create tcp transport: %s", err)
	}

	r, err := raft.NewRaft(cfg, rfsm, raftdb, raftdb, fss, transport)
	if err != nil {
		return nil, fmt.Errorf("raft.NewRaft: %v", err)
	}

	s.raft = r

	if raftBootstrap {
		cfg := raft.Configuration{
			Servers: []raft.Server{
				{
					Suffrage: raft.Voter,
					ID:       raft.ServerID(nodeId),
					Address:  raft.ServerAddress(raftAddr),
				},
			},
		}

		f := r.BootstrapCluster(cfg)
		if err := f.Error(); err != nil {
			return nil, fmt.Errorf("raft.Raft.BootstrapCluster: %v", err)
		}
	}

	go s.monitorLeadership()

	return s, nil
}

// refer: https://github.com/hashicorp/consul/blob/main/agent/consul/leader.go#L71
// monitorLeadership is used to monitor if we acquire or lose our role
// as the leader in the Raft cluster. There is some work the leader is
// expected to do, so we must react to changes
func (s *raftServer) monitorLeadership() {
	for {
		select {
		case isLeader := <-s.raftNotifyCh:
			switch {
			case isLeader:
				s.leaderLoop()
			}
		case <-s.shutdownCh:
			return
		}
	}
}

func (s *raftServer) leaderLoop() {
	// Apply a raft barrier to ensure our FSM is caught up
	barrier := s.raft.Barrier(barrierWriteTimeout)
	if err := barrier.Error(); err != nil {
		return
	}

	s.setConsistentReadReady()
}

// Atomically sets a readiness state flag when leadership is obtained, to indicate that server is past its barrier write
func (s *raftServer) setConsistentReadReady() {
	atomic.StoreInt32(&s.readyForConsistentReads, 1)
}

// Atomically reset readiness state flag on leadership revoke
func (s *raftServer) resetConsistentReadReady() {
	atomic.StoreInt32(&s.readyForConsistentReads, 0)
}

// Returns true if this server is ready to serve consistent reads
func (s *raftServer) isReadyForConsistentReads() bool {
	return atomic.LoadInt32(&s.readyForConsistentReads) == 1
}

func (s *raftServer) close() {
	close(s.shutdownCh)
	s.raft.Shutdown()
	s.raftdb.Close()
}
