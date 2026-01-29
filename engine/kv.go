package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/hashicorp/raft"
	"github.com/xkeyideal/raft-example/fsm"
	"github.com/xkeyideal/raft-example/service"
)

var _ service.KV = &raftServer{}

func ctxTimeout(ctx context.Context) time.Duration {
	deadline, _ := ctx.Deadline()
	timeout := time.Until(deadline)
	if timeout <= 0 {
		return 0
	}
	return timeout
}

func (s *raftServer) Apply(ctx context.Context, key, val string) (uint64, error) {
	// Note: We don't call ConsistentRead() here because Apply is a write operation.
	// raft.Apply() will automatically return ErrNotLeader if we're not the leader.
	// Calling ConsistentRead() before Apply would add unnecessary latency
	// (requires an extra quorum confirmation).

	payload := fsm.SetPayload{
		Key:   []byte(key),
		Value: []byte(val),
	}

	b, _ := json.Marshal(payload)

	c := &fsm.Command{
		Type:    fsm.Insert,
		Payload: b,
	}

	data, _ := json.Marshal(c)

	timeout := ctxTimeout(ctx)
	if timeout <= raftApplyTimeout {
		timeout = raftApplyTimeout
	}

	f := s.raft.Apply(data, timeout)
	if err := f.Error(); err != nil {
		return 0, fmt.Errorf("Apply %s: %s", s.raftAddr, err.Error())
	}

	s.dbAppliedIndex.Store(f.Index())

	res := f.Response().(*fsm.CommandResponse)
	if res.Error != nil {
		return 0, res.Error
	}

	return f.Index(), nil
}

func (s *raftServer) Query(ctx context.Context, key []byte, consistent bool) (uint64, []byte, error) {
	if !consistent {
		// Stale read: read directly from local store (may be stale)
		goto LOCAL_READ
	}

	// Linearizable read using VerifyLeader + local read
	// This is more efficient than going through Raft Apply for reads
	// because it doesn't write to the log, but still ensures we're the leader
	// and have applied all committed entries.
	//
	// The process:
	// 1. VerifyLeader() - confirms we're still the leader by contacting a quorum
	// 2. Check readyForConsistentReads - ensures we've applied the barrier after becoming leader
	// 3. Read locally - safe because we know we're up-to-date
	if err := s.ConsistentRead(); err != nil {
		return 0, nil, fmt.Errorf("ConsistentRead %s: %s", s.raftAddr, err.Error())
	}

	// After ConsistentRead succeeds, we can safely read from local store
	// because we've verified leadership and applied all committed entries

LOCAL_READ:

	res := s.store.ReadLocal(key)
	if res.Error != nil {
		return 0, nil, res.Error
	}

	return s.raft.AppliedIndex(), res.Val, nil
}

func (s *raftServer) GetLeader() (bool, raft.ServerAddress, error) {
	// Check if we are the leader
	if s.IsLeader() {
		return true, "", nil
	}

	// Get the leader
	leader, _ := s.raft.LeaderWithID()
	if leader == "" {
		return false, "", ErrNoLeader
	}

	return false, leader, nil
}

// IsLeader checks if this server is the cluster leader
func (s *raftServer) IsLeader() bool {
	return s.raft.State() == raft.Leader
}

// Stats returns stats for the store.
func (s *raftServer) Stats() (map[string]any, error) {
	nodes, err := s.Nodes()
	if err != nil {
		return nil, err
	}

	leaderAddr, leaderID := s.raft.LeaderWithID()

	// Perform type-conversion to actual numbers where possible.
	raftStats := make(map[string]any)
	for k, v := range s.raft.Stats() {
		if s, err := strconv.ParseInt(v, 10, 64); err != nil {
			raftStats[k] = v
		} else {
			raftStats[k] = s
		}
	}
	raftStats["log_size"], err = s.logSize()
	if err != nil {
		return nil, err
	}
	raftStats["voter"], err = s.IsVoter()
	if err != nil {
		return nil, err
	}

	dirSz, err := fsm.DirSize(s.raftDir)
	if err != nil {
		return nil, err
	}

	fsmStats, err := s.store.Stats()
	if err != nil {
		return nil, err
	}

	stats := map[string]any{
		"node_id":            s.raftId,
		"raft":               raftStats,
		"db_applied_index":   s.dbAppliedIndex.Load(),
		"last_applied_index": s.raft.AppliedIndex(),
		"addr":               s.raftAddr,
		"leader": map[string]string{
			"node_id": string(leaderID),
			"addr":    string(leaderAddr),
		},
		"observer": map[string]uint64{
			"observed": s.observer.GetNumObserved(),
			"dropped":  s.observer.GetNumDropped(),
		},
		"nodes":         nodes,
		"raft_dir":      s.raftDir,
		"raft_dir_size": dirSz,
		"fsm":           fsmStats,
	}
	return stats, nil
}

// Nodes returns the slice of nodes in the cluster, sorted by ID ascending.
func (s *raftServer) Nodes() ([]*Server, error) {
	f := s.raft.GetConfiguration()
	if f.Error() != nil {
		return nil, f.Error()
	}

	rs := f.Configuration().Servers
	servers := make([]*Server, len(rs))
	for i := range rs {
		servers[i] = &Server{
			ID:       string(rs[i].ID),
			Addr:     string(rs[i].Address),
			Suffrage: rs[i].Suffrage.String(),
		}
	}

	sort.Slice(servers, func(i, j int) bool {
		return servers[i].ID < servers[j].ID
	})

	return servers, nil
}

type Server struct {
	ID       string `json:"id,omitempty"`
	Addr     string `json:"addr,omitempty"`
	Suffrage string `json:"suffrage,omitempty"`
}
