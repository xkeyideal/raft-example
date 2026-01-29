package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/xkeyideal/raft-example/engine"
)

var (
	raftAddr      = flag.String("raft_addr", "localhost:50051", "TCP host+port for this node")
	grpcAddr      = flag.String("grpc_addr", "localhost:40051", "GRPC host+port for this node")
	raftId        = flag.String("raft_id", "", "Node id used by Raft")
	raftDir       = flag.String("raft_data_dir", "/tmp", "Raft data dir")
	raftBootstrap = flag.Bool("raft_bootstrap", false, "Whether to bootstrap the Raft cluster")

	// Gossip flags for dynamic service discovery (optional)
	gossipAddr  = flag.String("gossip_addr", "", "Gossip bind address (e.g., 0.0.0.0:7946). Empty to disable gossip.")
	gossipSeeds = flag.String("gossip_seeds", "", "Comma-separated list of gossip seed nodes (e.g., 192.168.1.1:7946,192.168.1.2:7946)")
)

func main() {
	flag.Parse()

	if *raftId == "" {
		log.Fatalf("flag --raft_id is required")
	}

	if *raftAddr == "" {
		log.Fatalf("flag --raft_addr is required")
	}

	if *grpcAddr == "" {
		log.Fatalf("flag --grpc_addr is required")
	}

	cfg := engine.EngineConfig{
		RaftDir:       *raftDir,
		NodeID:        *raftId,
		RaftAddr:      *raftAddr,
		GRPCAddr:      *grpcAddr,
		RaftBootstrap: *raftBootstrap,
		GossipEnabled: false,
	}

	// Enable gossip if gossip_addr is provided
	if *gossipAddr != "" {
		cfg.GossipEnabled = true
		cfg.GossipAddr = *gossipAddr
		if *gossipSeeds != "" {
			cfg.GossipSeeds = strings.Split(*gossipSeeds, ",")
		}
		log.Printf("[INFO] Gossip enabled, bind=%s seeds=%v", *gossipAddr, cfg.GossipSeeds)
	}

	e, err := engine.NewEngineWithConfig(cfg)
	if err != nil {
		panic(err)
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	log.Println(<-signals)

	e.Close()
}
