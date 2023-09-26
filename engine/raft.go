package engine

import (
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/atomic"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/raft"

	"github.com/xkeyideal/raft-example/fsm"
	pebbledb "github.com/xkeyideal/raft-pebbledb"
)

const (
	barrierWriteTimeout = 2 * time.Minute
	grpcConnectTimeout  = 3 * time.Second
	appliedWaitDelay    = 100 * time.Millisecond
	observerChanLen     = 50

	raftDBPath = "raftdb"
)

type raftServer struct {
	raftId   string
	raftAddr string
	raftDir  string

	dbAppliedIndex *atomic.Uint64
	store          *fsm.StateMachine
	raft           *raft.Raft
	raftdb         *pebbledb.PebbleStore

	// raftNotifyCh ensures that we get reliable leader
	// transition notifications from the Raft layer.
	// just leader node receive the notify channel
	// if want per node can receive leader changes can use RegisterObserver
	raftNotifyCh chan bool

	// readyForConsistentReads is used to track when the leader server is
	// ready to serve consistent reads, after it has applied its initial
	// barrier. This is updated atomically.
	readyForConsistentReads *atomic.Int32

	shutdownCh chan struct{}

	// Raft changes observer
	observerChan chan raft.Observation
	observer     *raft.Observer
}

func newRaft(baseDir, nodeId, raftAddr string, rfsm *fsm.StateMachine, raftBootstrap bool) (*raftServer, error) {
	s := &raftServer{
		raftId:                  nodeId,
		raftAddr:                raftAddr,
		raftDir:                 baseDir,
		dbAppliedIndex:          atomic.NewUint64(0),
		readyForConsistentReads: atomic.NewInt32(0),
		store:                   rfsm,
		raftNotifyCh:            make(chan bool, 10),
		shutdownCh:              make(chan struct{}),
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
		NotifyCh:           s.raftNotifyCh,
	}

	raftdb, err := pebbledb.NewPebbleStore(filepath.Join(baseDir, raftDBPath), &fsm.Logger{}, pebbledb.DefaultPebbleDBConfig())
	if err != nil {
		return nil, fmt.Errorf(`pebbledb.NewPebbleStore(%q): %v`, filepath.Join(baseDir, raftDBPath), err)
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

	// registers an Observer with raft in order to receive updates
	// about leader changes, in order to keep the grpc resolver up to date for leader forwarding.
	s.observerChan = make(chan raft.Observation, observerChanLen)
	s.observer = raft.NewObserver(s.observerChan, false, func(o *raft.Observation) bool {
		_, isLeaderChange := o.Data.(raft.LeaderObservation)
		_, isFailedHeartBeat := o.Data.(raft.FailedHeartbeatObservation)
		return isLeaderChange || isFailedHeartBeat
	})

	// Register and listen for leader changes.
	s.raft.RegisterObserver(s.observer)
	s.observe()

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

	s.store.UpdateFsmIndex(s.raft.AppliedIndex())

	go s.monitorLeadership()

	return s, nil
}

// DeregisterObserver deregisters an observer of Raft events
func (s *raftServer) DeregisterObserver(o *raft.Observer) {
	s.raft.DeregisterObserver(o)
}

func (s *raftServer) observe() {
	go func() {
		for {
			select {
			case o := <-s.observerChan:
				switch signal := o.Data.(type) {
				case raft.FailedHeartbeatObservation:
					log.Println("FailedHeartbeatObservation", signal)
				case raft.LeaderObservation:
					log.Println("LeaderObservation", signal)
					// s.selfLeaderChange(signal.LeaderID == raft.ServerID(s.raftID))
				}

			case <-s.shutdownCh:
				s.raft.DeregisterObserver(s.observer)
				return
			}
		}
	}()
}

// refer: https://github.com/hashicorp/consul/blob/main/agent/consul/leader.go#L71
// monitorLeadership is used to monitor if we acquire or lose our role
// as the leader in the Raft cluster. There is some work the leader is
// expected to do, so we must react to changes
func (s *raftServer) monitorLeadership() {
	for {
		select {
		case isLeader := <-s.raftNotifyCh:
			log.Println("monitorLeadership", isLeader)
			switch {
			case isLeader:
				s.leaderLoop()
			}
		case <-s.shutdownCh:
			return
		}
	}
}

// logSize returns the size of the Raft log on disk.
func (s *raftServer) logSize() (int64, error) {
	fi, err := os.Stat(filepath.Join(s.raftDir, raftDBPath))
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// IsVoter returns true if the current node is a voter in the cluster. If there
// is no reference to the current node in the current cluster configuration then
// false will also be returned.
func (s *raftServer) IsVoter() (bool, error) {
	cfg := s.raft.GetConfiguration()
	if err := cfg.Error(); err != nil {
		return false, err
	}
	for _, srv := range cfg.Configuration().Servers {
		if srv.ID == raft.ServerID(s.raftId) {
			return srv.Suffrage == raft.Voter, nil
		}
	}
	return false, nil
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
	s.readyForConsistentReads.Store(1)
}

// Atomically reset readiness state flag on leadership revoke
func (s *raftServer) resetConsistentReadReady() {
	s.readyForConsistentReads.Store(0)
}

// Returns true if this server is ready to serve consistent reads
func (s *raftServer) isReadyForConsistentReads() bool {
	return s.readyForConsistentReads.Load() == 1
}

func (s *raftServer) close() {
	close(s.shutdownCh)
	s.raft.Shutdown()
	s.raftdb.Close()
}
