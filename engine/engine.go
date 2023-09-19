package engine

import (
	"fmt"
	"log"
	"net"
	"os"
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
	raft       *hraft
	fsm        *fsm.StateMachine
	grpcServer *grpc.Server
}

func NewEngine(raftDir, nodeId, raftAddr, grpcAddr string, raftBootstrap bool) (*Engine, error) {
	baseDir := filepath.Join(raftDir, nodeId)

	if err := os.MkdirAll(baseDir, os.ModePerm); err != nil {
		return nil, err
	}

	store, err := fsm.NewStore(baseDir)
	if err != nil {
		return nil, err
	}

	fsm := fsm.NewStateMachine(raftAddr, nodeId, store)
	r, err := NewRaft(baseDir, nodeId, raftAddr, fsm, raftBootstrap)
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
	pb.RegisterExampleServer(s, service.NewGrpcService(fsm, r.r))
	raftmanager.Register(s, r.r)

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
