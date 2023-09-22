package service

import (
	"context"
	"time"

	"github.com/xkeyideal/raft-example/fsm"
	pb "github.com/xkeyideal/raft-example/proto"
)

type KV interface {
	Apply(key, val string, timeout time.Duration) (uint64, error)
	Query(key []byte, consistent bool, timeout time.Duration) (uint64, []byte, error)
}

type GRPCService struct {
	pb.UnimplementedExampleServer
	fsm *fsm.StateMachine
	kv  KV
}

func NewGrpcService(fsm *fsm.StateMachine, kv KV) *GRPCService {
	return &GRPCService{
		fsm: fsm,
		kv:  kv,
	}
}

func (r *GRPCService) Add(ctx context.Context, req *pb.AddRequest) (*pb.AddResponse, error) {
	index, err := r.kv.Apply(req.Key, req.Val, time.Second)
	if err != nil {
		return nil, err
	}

	return &pb.AddResponse{
		CommitIndex: index,
	}, nil
}

func (r *GRPCService) Get(ctx context.Context, req *pb.GetRequest) (*pb.GetResponse, error) {
	index, val, err := r.kv.Query([]byte(req.Key), req.Linearizable, time.Second)
	if err != nil {
		return nil, err
	}

	return &pb.GetResponse{
		Value:       string(val),
		ReadAtIndex: index,
	}, nil
}
