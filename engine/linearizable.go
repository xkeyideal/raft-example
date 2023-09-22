package engine

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	rpcHoldTimeout = 5 * time.Second

	// JitterFraction is a the limit to the amount of jitter we apply
	// to a user specified MaxQueryTime. We divide the specified time by
	// the fraction. So 16 == 6.25% limit of jitter. This same fraction
	// is applied to the RPCHoldTimeout
	jitterFraction = 16
)

var (
	ErrNotReadyForConsistentReads = errors.New("Not ready to serve consistent reads")
	ErrNoLeader                   = errors.New("No cluster leader")
)

// ConsistentRead is used to ensure we do not perform a stale
// read. This is done by verifying leadership before the read.
func (s *raftServer) ConsistentRead() error {
	ctx, cancel := context.WithTimeout(context.Background(), rpcHoldTimeout)
	defer cancel()

	return s.consistentReadWithContext(ctx)
}

func (s *raftServer) consistentReadWithContext(ctx context.Context) error {
	future := s.raft.VerifyLeader()
	if err := future.Error(); err != nil {
		return err // fail fast if leader verification fails
	}

	if s.isReadyForConsistentReads() {
		return nil
	}

	// Poll until the context reaches its deadline, or for RPCHoldTimeout if the
	// context has no deadline.
	pollFor := rpcHoldTimeout
	if deadline, ok := ctx.Deadline(); ok {
		pollFor = time.Until(deadline)
	}

	interval := pollFor / jitterFraction
	if interval <= 0 {
		return ErrNotReadyForConsistentReads
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if s.isReadyForConsistentReads() {
				return nil
			}
		case <-ctx.Done():
			return ErrNotReadyForConsistentReads
		case <-s.shutdownCh:
			return fmt.Errorf("shutdown waiting for leader")
		}
	}
}
