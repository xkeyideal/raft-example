package engine

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/hashicorp/raft"

	"github.com/xkeyideal/raft-example/fsm"
	pebbledb "github.com/xkeyideal/raft-pebbledb"
)

type hraft struct {
	r      *raft.Raft
	raftdb *pebbledb.PebbleStore
}

func newRaft(baseDir, nodeId, raftAddr string, rfsm raft.FSM, raftBootstrap bool) (*hraft, error) {
	c := raft.DefaultConfig()
	c.LocalID = raft.ServerID(nodeId)

	raftdb, err := pebbledb.NewPebbleStore(filepath.Join(baseDir, "raftdb"), &fsm.Logger{}, pebbledb.DefaultPebbleDBConfig())
	if err != nil {
		return nil, fmt.Errorf(`pebbledb.NewPebbleStore(%q): %v`, filepath.Join(baseDir, "raftdb"), err)
	}

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

	r, err := raft.NewRaft(c, rfsm, raftdb, raftdb, fss, transport)
	if err != nil {
		return nil, fmt.Errorf("raft.NewRaft: %v", err)
	}

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

	return &hraft{
		r:      r,
		raftdb: raftdb,
	}, nil
}

func (r *hraft) close() {
	r.r.Shutdown()
	r.raftdb.Close()
}
