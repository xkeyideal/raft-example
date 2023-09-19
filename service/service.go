package service

import (
	"context"
	"time"

	"github.com/hashicorp/raft"
	"github.com/xkeyideal/raft-example/fsm"
	pb "github.com/xkeyideal/raft-example/proto"
)

type GRPCService struct {
	pb.UnimplementedExampleServer
	fsm  *fsm.StateMachine
	raft *raft.Raft
}

func NewGrpcService(fsm *fsm.StateMachine, raft *raft.Raft) *GRPCService {
	return &GRPCService{
		fsm:  fsm,
		raft: raft,
	}
}

func (r *GRPCService) AddWord(ctx context.Context, req *pb.AddWordRequest) (*pb.AddWordResponse, error) {
	f := r.raft.Apply([]byte(req.GetWord()), time.Second)
	if err := f.Error(); err != nil {
		return nil, err
	}
	return &pb.AddWordResponse{
		CommitIndex: f.Index(),
	}, nil
}

func (r *GRPCService) GetWords(ctx context.Context, req *pb.GetWordsRequest) (*pb.GetWordsResponse, error) {
	val, err := r.fsm.Lookup([]byte(req.Key))
	if err != nil {
		return nil, err
	}

	return &pb.GetWordsResponse{
		Value:       string(val),
		ReadAtIndex: r.raft.AppliedIndex(),
	}, nil
}
