package engine

import (
	"encoding/json"
	"time"

	"github.com/xkeyideal/raft-example/fsm"
	"github.com/xkeyideal/raft-example/service"
)

var _ service.KV = &raftServer{}

func (s *raftServer) Apply(key, val string, timeout time.Duration) (uint64, error) {
	payload := fsm.SetPayload{
		Key:   key,
		Value: val,
	}

	b, _ := json.Marshal(payload)

	f := s.raft.Apply(b, timeout)
	if err := f.Error(); err != nil {
		return 0, err
	}

	return f.Index(), nil
}

func (s *raftServer) Query(key []byte, consistent bool, timeout time.Duration) (uint64, []byte, error) {
	if consistent {
		if err := s.ConsistentRead(); err != nil {
			return 0, nil, err
		}
	}

	val, err := s.store.Lookup(key)
	if err != nil {
		return 0, nil, err
	}

	return s.raft.AppliedIndex(), val, nil
}
