package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/xkeyideal/raft-example/engine"
)

var (
	raftAddr      = flag.String("raft_addr", "localhost:50051", "TCP host+port for this node")
	grpcAddr      = flag.String("grpc_addr", "localhost:40051", "GRPC host+port for this node")
	raftId        = flag.String("raft_id", "", "Node id used by Raft")
	raftDir       = flag.String("raft_data_dir", "/tmp", "Raft data dir")
	raftBootstrap = flag.Bool("raft_bootstrap", false, "Whether to bootstrap the Raft cluster")
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

	engine, err := engine.NewEngine(*raftDir, *raftId, *raftAddr, *grpcAddr, *raftBootstrap)
	if err != nil {
		panic(err)
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL)

	log.Println(<-signals)

	engine.Close()
}
