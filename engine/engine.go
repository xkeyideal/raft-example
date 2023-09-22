package engine

import (
	"fmt"
	"log"
	"net"
	"path/filepath"
	"time"

	"github.com/xkeyideal/raft-example/fsm"
	pb "github.com/xkeyideal/raft-example/proto"
	"github.com/xkeyideal/raft-example/service"
	raftmanager "github.com/xkeyideal/raft-manager"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

type Engine struct {
	raft       *raftServer
	fsm        *fsm.StateMachine
	grpcServer *grpc.Server
}

func NewEngine(raftDir, nodeId, raftAddr, grpcAddr string, raftBootstrap bool) (*Engine, error) {
	userDir := filepath.Join(raftDir, nodeId, "user_data")
	store, err := fsm.NewStore(userDir)
	if err != nil {
		return nil, err
	}

	fsm := fsm.NewStateMachine(raftAddr, nodeId, store)

	metaDir := filepath.Join(raftDir, nodeId, "meta_data")
	r, err := newRaft(metaDir, nodeId, raftAddr, fsm, raftBootstrap)
	if err != nil {
		return nil, err
	}

	kaep := keepalive.EnforcementPolicy{
		MinTime:             5 * time.Second,
		PermitWithoutStream: true,
	}

	kasp := keepalive.ServerParameters{
		Time:                  10 * time.Second,
		Timeout:               2 * time.Second,
		MaxConnectionIdle:     15 * time.Second,
		MaxConnectionAge:      30 * time.Second,
		MaxConnectionAgeGrace: 5 * time.Second,
	}

	gopts := []grpc.ServerOption{
		grpc.KeepaliveEnforcementPolicy(kaep),
		grpc.KeepaliveParams(kasp),
	}

	s := grpc.NewServer(gopts...)
	pb.RegisterExampleServer(s, service.NewGrpcService(fsm, r))
	raftmanager.Register(s, r.raft)

	e := &Engine{
		raft:       r,
		fsm:        fsm,
		grpcServer: s,
	}

	_, port, err := net.SplitHostPort(grpcAddr)
	if err != nil {
		log.Fatalf("failed to parse grpc address (%q): %v", grpcAddr, err)
	}

	sock, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	go func() {
		if err := s.Serve(sock); err != nil {
			log.Fatalf("[ERROR] grpc start %s\n", err.Error())
		}
	}()

	return e, nil
}
func (engine *Engine) Close() {
	if engine.grpcServer != nil {
		engine.grpcServer.Stop()
	}

	engine.raft.close()
	engine.fsm.Close()
}
