package engine

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.uber.org/atomic"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/raft"

	"github.com/xkeyideal/raft-example/fsm"
	pebbledb "github.com/xkeyideal/raft-pebbledb/v2"
)

const (
	barrierWriteTimeout = 2 * time.Minute
	grpcConnectTimeout  = 3 * time.Second
	appliedWaitDelay    = 100 * time.Millisecond
	observerChanLen     = 50

	raftDBPath       = "raftdb"
	raftSnapshotPath = "raft-snapshot"
)

type raftServer struct {
	raftId   string
	raftAddr string
	raftDir  string

	dbAppliedIndex *atomic.Uint64
	store          *fsm.StateMachine
	raft           *raft.Raft
	raftdb         *pebbledb.PebbleStore
	transport      *raft.NetworkTransport

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

	// leaderLoopCancel is used to signal the leader loop to stop
	leaderLoopCancel chan struct{}

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
		ProtocolVersion:          raft.ProtocolVersionMax,
		HeartbeatTimeout:         1000 * time.Millisecond,
		ElectionTimeout:          1000 * time.Millisecond,
		CommitTimeout:            50 * time.Millisecond,
		MaxAppendEntries:         64,
		BatchApplyCh:             true,
		ShutdownOnRemove:         true,
		TrailingLogs:             10240,
		SnapshotInterval:         120 * time.Second,
		SnapshotThreshold:        8192,
		LeaderLeaseTimeout:       500 * time.Millisecond,
		LogLevel:                 hclog.Warn.String(),
		LocalID:                  raft.ServerID(nodeId),
		NotifyCh:                 s.raftNotifyCh,
		NoSnapshotRestoreOnStart: false,
	}

	raftdb, err := pebbledb.NewPebbleStore(filepath.Join(baseDir, raftDBPath), &fsm.Logger{}, pebbledb.DefaultPebbleDBConfig())
	if err != nil {
		return nil, fmt.Errorf(`pebbledb.NewPebbleStore(%q): %v`, filepath.Join(baseDir, raftDBPath), err)
	}

	s.raftdb = raftdb

	snapshotDir := filepath.Join(baseDir, raftSnapshotPath)
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

	s.transport = transport

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
	go s.observe()

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
}

// refer: https://github.com/hashicorp/consul/blob/main/agent/consul/leader.go#L71
// monitorLeadership is used to monitor if we acquire or lose our role
// as the leader in the Raft cluster. There is some work the leader is
// expected to do, so we must react to changes
func (s *raftServer) monitorLeadership() {
	var leaderLoopWg sync.WaitGroup
	var leaderLoopCancel chan struct{}

	for {
		select {
		case isLeader := <-s.raftNotifyCh:
			log.Println("monitorLeadership", isLeader)
			switch {
			case isLeader:
				if leaderLoopCancel != nil {
					log.Println("[WARN] attempted to start leader loop while already running")
					continue
				}
				leaderLoopCancel = make(chan struct{})
				leaderLoopWg.Add(1)
				go func(stopCh chan struct{}) {
					defer leaderLoopWg.Done()
					s.leaderLoop(stopCh)
				}(leaderLoopCancel)
				log.Println("[INFO] cluster leadership acquired")

			default:
				if leaderLoopCancel == nil {
					log.Println("[WARN] attempted to stop leader loop while not running")
					continue
				}
				log.Println("[INFO] shutting down leader loop")
				close(leaderLoopCancel)
				leaderLoopWg.Wait()
				leaderLoopCancel = nil
				// Note: revokeLeadership is called inside leaderLoop via defer
				// when establishLeadership succeeds. If it failed, revokeLeadership
				// was already called there. So we don't need to call it here again.
				log.Println("[INFO] cluster leadership lost")
			}
		case <-s.shutdownCh:
			if leaderLoopCancel != nil {
				close(leaderLoopCancel)
				leaderLoopWg.Wait()
			}
			return
		}
	}
}

// logSize returns the size of the Raft log on disk.
func (s *raftServer) logSize() (int64, error) {
	return fsm.DirSize(filepath.Join(s.raftDir, raftDBPath))
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

func (s *raftServer) leaderLoop(stopCh chan struct{}) {
	// Create a context that will be cancelled when we lose leadership
	stopCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		<-stopCh
		cancel()
	}()

	establishedLeader := false

RECONCILE:
	// Apply a raft barrier to ensure our FSM is caught up
	start := time.Now()
	barrier := s.raft.Barrier(barrierWriteTimeout)
	if err := barrier.Error(); err != nil {
		log.Printf("[ERROR] failed to wait for barrier: %v", err)
		goto WAIT
	}
	log.Printf("[INFO] leader barrier completed in %v", time.Since(start))

	// Check if we need to handle initial leadership actions
	if !establishedLeader {
		if err := s.establishLeadership(stopCtx); err != nil {
			log.Printf("[ERROR] failed to establish leadership: %v", err)
			// Immediately revoke leadership since we didn't successfully
			// establish leadership.
			s.revokeLeadership()

			// Attempt to transfer leadership. If successful it is
			// time to leave the leaderLoop since this node is no
			// longer the leader. If leadershipTransfer() fails, we
			// will try to acquire it again after 5 seconds.
			if err := s.leadershipTransfer(); err != nil {
				log.Printf("[ERROR] failed to transfer leadership: %v", err)
				goto WAIT
			}
			return
		}
		establishedLeader = true
		defer s.revokeLeadership()
	}

	// Wait for leadership loss or shutdown
	<-stopCh
	return

WAIT:
	// Poll the stop channel to give it priority so we don't waste time
	// trying to perform the other operations if we have been asked to shut down.
	select {
	case <-stopCh:
		return
	default:
	}

	// Wait before retrying
	select {
	case <-stopCh:
		return
	case <-time.After(5 * time.Second):
		goto RECONCILE
	}
}

// establishLeadership is invoked once we become leader and are able
// to invoke an initial barrier. The barrier is used to ensure any
// previously inflight transactions have been committed and that our
// state is up-to-date.
func (s *raftServer) establishLeadership(ctx context.Context) error {
	start := time.Now()

	// Here you can add any leader-specific initialization:
	// - Initialize background tasks
	// - Start replication routines
	// - Setup session timers
	// - etc.

	// Example: Check if context is cancelled during initialization
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// Mark server as ready for consistent reads
	s.setConsistentReadReady()

	log.Printf("[INFO] successfully established leadership in %v", time.Since(start))
	return nil
}

// leadershipTransfer attempts to transfer leadership to another server.
// Returns nil on success, error on failure.
func (s *raftServer) leadershipTransfer() error {
	retryCount := 3
	for i := 0; i < retryCount; i++ {
		future := s.raft.LeadershipTransfer()
		if err := future.Error(); err != nil {
			log.Printf("[ERROR] failed to transfer leadership (attempt %d/%d): %v", i+1, retryCount, err)
			continue
		}
		log.Printf("[INFO] successfully transferred leadership (attempt %d/%d)", i+1, retryCount)
		return nil
	}
	return fmt.Errorf("failed to transfer leadership in %d attempts", retryCount)
}

// Atomically sets a readiness state flag when leadership is obtained, to indicate that server is past its barrier write
func (s *raftServer) setConsistentReadReady() {
	s.readyForConsistentReads.Store(1)
}

// Atomically reset readiness state flag on leadership revoke
func (s *raftServer) resetConsistentReadReady() {
	s.readyForConsistentReads.Store(0)
}

// revokeLeadership is called when we step down as leader.
// This is used to cleanup any state that may be specific to a leader.
func (s *raftServer) revokeLeadership() {
	s.resetConsistentReadReady()
	// Add any additional cleanup here (e.g., stop background tasks)
}

// Returns true if this server is ready to serve consistent reads
func (s *raftServer) isReadyForConsistentReads() bool {
	return s.readyForConsistentReads.Load() == 1
}

func (s *raftServer) close() {
	close(s.shutdownCh)
	s.raft.Shutdown()
	if s.transport != nil {
		s.transport.Close()
	}
	s.raftdb.Close()
}
