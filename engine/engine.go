package engine

import (
	"context"
	"fmt"
	"log"
	"net"
	"path/filepath"
	"sync"
	"time"

	"github.com/xkeyideal/raft-example/fsm"
	"github.com/xkeyideal/raft-example/gossip"
	pb "github.com/xkeyideal/raft-example/proto"
	"github.com/xkeyideal/raft-example/resolver"
	"github.com/xkeyideal/raft-example/service"
	raftmanager "github.com/xkeyideal/raft-manager"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

// EngineConfig holds the configuration for the Engine.
type EngineConfig struct {
	RaftDir       string
	NodeID        string
	RaftAddr      string
	GRPCAddr      string
	RaftBootstrap bool

	// Gossip configuration (optional, set GossipEnabled=true to use)
	GossipEnabled bool
	GossipAddr    string // e.g., "0.0.0.0:7946"
	GossipSeeds   []string
}

type Engine struct {
	raft       *raftServer
	fsm        *fsm.StateMachine
	grpcServer *grpc.Server
	listener   net.Listener
	gossip     *gossip.Gossip
	resolver   resolver.AddressResolver

	// Graceful shutdown
	shutdownOnce sync.Once
	shutdownCh   chan struct{}
	wg           sync.WaitGroup
}

// NewEngineWithConfig creates a new Engine with the given configuration.
func NewEngineWithConfig(cfg EngineConfig) (*Engine, error) {
	fsm, err := fsm.NewStateMachine(cfg.RaftAddr, cfg.NodeID, filepath.Join(cfg.RaftDir, cfg.NodeID, "user_data"))
	if err != nil {
		return nil, err
	}

	metaDir := filepath.Join(cfg.RaftDir, cfg.NodeID, "meta_data")
	r, err := newRaft(metaDir, cfg.NodeID, cfg.RaftAddr, fsm, cfg.RaftBootstrap)
	if err != nil {
		return nil, err
	}

	e := &Engine{
		raft:       r,
		fsm:        fsm,
		shutdownCh: make(chan struct{}),
	}

	// Setup address resolver (gossip or static)
	if cfg.GossipEnabled {
		g, err := e.setupGossip(cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to setup gossip: %v", err)
		}
		e.gossip = g
		e.resolver = resolver.NewGossipResolver(g)
	} else {
		e.resolver = resolver.NewStaticResolver(nil) // Will use default in service
	}

	// Setup gRPC server
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

	// Use resolver-aware service if gossip is enabled
	if cfg.GossipEnabled {
		pb.RegisterExampleServer(s, service.NewGrpcServiceWithResolver(fsm, r, e.resolver))
	} else {
		pb.RegisterExampleServer(s, service.NewGrpcService(fsm, r))
	}
	raftmanager.Register(s, r.raft)

	e.grpcServer = s

	_, port, err := net.SplitHostPort(cfg.GRPCAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse grpc address (%q): %v", cfg.GRPCAddr, err)
	}

	sock, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		return nil, fmt.Errorf("failed to listen: %v", err)
	}
	e.listener = sock

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		if err := s.Serve(sock); err != nil {
			select {
			case <-e.shutdownCh:
				// Expected during shutdown
			default:
				log.Printf("[ERROR] grpc serve: %v", err)
			}
		}
	}()

	return e, nil
}

// setupGossip initializes the gossip service for dynamic node discovery.
func (e *Engine) setupGossip(cfg EngineConfig) (*gossip.Gossip, error) {
	host, port, err := gossip.ParseHostPort(cfg.GossipAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid gossip address: %v", err)
	}

	gossipCfg := gossip.Config{
		BindAddr: host,
		BindPort: port,
		Seeds:    cfg.GossipSeeds,
		NodeMeta: gossip.NodeMeta{
			NodeID:   cfg.NodeID,
			RaftAddr: cfg.RaftAddr,
			GRPCAddr: cfg.GRPCAddr,
		},
	}

	g, err := gossip.New(gossipCfg)
	if err != nil {
		return nil, err
	}

	// Log node join/leave events
	g.OnJoin(func(meta gossip.NodeMeta) {
		log.Printf("[INFO] engine: node discovered via gossip: %s (raft=%s, grpc=%s)",
			meta.NodeID, meta.RaftAddr, meta.GRPCAddr)
	})

	g.OnLeave(func(meta gossip.NodeMeta) {
		log.Printf("[INFO] engine: node left via gossip: %s (raft=%s, grpc=%s)",
			meta.NodeID, meta.RaftAddr, meta.GRPCAddr)
	})

	return g, nil
}

// Close gracefully shuts down the Engine.
func (e *Engine) Close() {
	e.shutdownOnce.Do(func() {
		close(e.shutdownCh)

		// Close cached gRPC client connections
		service.CloseAllConns()

		// Gracefully stop gRPC server (wait for ongoing requests)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		done := make(chan struct{})
		go func() {
			e.grpcServer.GracefulStop()
			close(done)
		}()

		select {
		case <-done:
			log.Println("[INFO] gRPC server stopped gracefully")
		case <-ctx.Done():
			log.Println("[WARN] gRPC server graceful stop timeout, forcing stop")
			e.grpcServer.Stop()
		}

		if e.listener != nil {
			e.listener.Close()
		}

		// Leave gossip cluster gracefully
		if e.gossip != nil {
			if err := e.gossip.Leave(5 * time.Second); err != nil {
				log.Printf("[WARN] gossip leave failed: %v", err)
			}
			e.gossip.Shutdown()
		}

		e.raft.close()
		e.fsm.Close()

		// Wait for all goroutines
		e.wg.Wait()
	})
}

// GetResolver returns the address resolver used by this engine.
func (e *Engine) GetResolver() resolver.AddressResolver {
	return e.resolver
}

// GetGossip returns the gossip instance (nil if gossip is disabled).
func (e *Engine) GetGossip() *gossip.Gossip {
	return e.gossip
}
