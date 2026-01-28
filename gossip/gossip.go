// Package gossip provides dynamic node discovery using HashiCorp's memberlist.
// This allows nodes to discover each other's RPC addresses without hardcoding,
// and propagates cluster membership changes automatically.
package gossip

import (
	"encoding/json"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/hashicorp/memberlist"
)

// NodeMeta contains the metadata about a node that is propagated via gossip.
type NodeMeta struct {
	NodeID   string `json:"node_id"`
	RaftAddr string `json:"raft_addr"`
	GRPCAddr string `json:"grpc_addr"`
}

// Config holds the configuration for the gossip service.
type Config struct {
	BindAddr      string   // BindAddr is the address to bind the gossip listener to.
	BindPort      int      // BindPort is the port to bind the gossip listener to.
	AdvertiseAddr string   // AdvertiseAddr is the address to advertise to other nodes.
	AdvertisePort int      // AdvertisePort is the port to advertise to other nodes.
	Seeds         []string // Seeds are the initial nodes to join (can be empty for bootstrap node).
	NodeMeta      NodeMeta // NodeMeta contains this node's metadata.
}

// Gossip manages cluster membership using memberlist.
type Gossip struct {
	mu         sync.RWMutex
	memberlist *memberlist.Memberlist
	localMeta  NodeMeta
	nodes      map[string]NodeMeta // nodeID -> NodeMeta

	// Callbacks
	onJoin  func(NodeMeta)
	onLeave func(NodeMeta)
}

// eventDelegate handles memberlist events.
type eventDelegate struct {
	g *Gossip
}

func (e *eventDelegate) NotifyJoin(node *memberlist.Node) {
	if len(node.Meta) == 0 {
		return
	}
	var meta NodeMeta
	if err := json.Unmarshal(node.Meta, &meta); err != nil {
		log.Printf("[WARN] gossip: failed to unmarshal node meta: %v", err)
		return
	}
	e.g.mu.Lock()
	e.g.nodes[meta.NodeID] = meta
	e.g.mu.Unlock()
	log.Printf("[INFO] gossip: node joined: %s (raft=%s, grpc=%s)", meta.NodeID, meta.RaftAddr, meta.GRPCAddr)
	if e.g.onJoin != nil {
		e.g.onJoin(meta)
	}
}

func (e *eventDelegate) NotifyLeave(node *memberlist.Node) {
	if len(node.Meta) == 0 {
		return
	}
	var meta NodeMeta
	if err := json.Unmarshal(node.Meta, &meta); err != nil {
		log.Printf("[WARN] gossip: failed to unmarshal node meta: %v", err)
		return
	}
	e.g.mu.Lock()
	delete(e.g.nodes, meta.NodeID)
	e.g.mu.Unlock()
	log.Printf("[INFO] gossip: node left: %s (raft=%s, grpc=%s)", meta.NodeID, meta.RaftAddr, meta.GRPCAddr)
	if e.g.onLeave != nil {
		e.g.onLeave(meta)
	}
}

func (e *eventDelegate) NotifyUpdate(node *memberlist.Node) {
	e.NotifyJoin(node) // Treat update as a re-join
}

// nodeDelegate handles node metadata.
type nodeDelegate struct {
	g *Gossip
}

func (d *nodeDelegate) NodeMeta(limit int) []byte {
	data, _ := json.Marshal(d.g.localMeta)
	return data
}

func (d *nodeDelegate) NotifyMsg([]byte)                           {}
func (d *nodeDelegate) GetBroadcasts(overhead, limit int) [][]byte { return nil }
func (d *nodeDelegate) LocalState(join bool) []byte                { return nil }
func (d *nodeDelegate) MergeRemoteState(buf []byte, join bool)     {}

// logWriter wraps log output with a prefix.
type logWriter struct {
	prefix string
}

func (l *logWriter) Write(p []byte) (n int, err error) {
	log.Printf("%s%s", l.prefix, string(p))
	return len(p), nil
}

// New creates a new Gossip instance and starts the memberlist.
func New(cfg Config) (*Gossip, error) {
	g := &Gossip{
		localMeta: cfg.NodeMeta,
		nodes:     make(map[string]NodeMeta),
	}
	// Add self to nodes
	g.nodes[cfg.NodeMeta.NodeID] = cfg.NodeMeta

	mlConfig := memberlist.DefaultLANConfig()
	mlConfig.Name = cfg.NodeMeta.NodeID
	mlConfig.BindAddr = cfg.BindAddr
	mlConfig.BindPort = cfg.BindPort
	if cfg.AdvertiseAddr != "" {
		mlConfig.AdvertiseAddr = cfg.AdvertiseAddr
	}
	if cfg.AdvertisePort > 0 {
		mlConfig.AdvertisePort = cfg.AdvertisePort
	}
	mlConfig.Delegate = &nodeDelegate{g: g}
	mlConfig.Events = &eventDelegate{g: g}
	// Reduce log noise
	mlConfig.LogOutput = &logWriter{prefix: "[gossip] "}

	ml, err := memberlist.Create(mlConfig)
	if err != nil {
		return nil, err
	}
	g.memberlist = ml

	// Join seeds if provided
	if len(cfg.Seeds) > 0 {
		go func() {
			// Retry joining seeds with backoff
			for i := 0; i < 5; i++ {
				if n, err := ml.Join(cfg.Seeds); err != nil {
					log.Printf("[WARN] gossip: failed to join seeds (attempt %d): %v", i+1, err)
					time.Sleep(time.Duration(i+1) * time.Second)
				} else {
					log.Printf("[INFO] gossip: joined %d seed nodes", n)
					return
				}
			}
		}()
	}

	return g, nil
}

// OnJoin sets a callback that is called when a node joins.
func (g *Gossip) OnJoin(fn func(NodeMeta)) {
	g.onJoin = fn
}

// OnLeave sets a callback that is called when a node leaves.
func (g *Gossip) OnLeave(fn func(NodeMeta)) {
	g.onLeave = fn
}

// GetGRPCAddr returns the gRPC address for a given node ID.
func (g *Gossip) GetGRPCAddr(nodeID string) (string, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	meta, ok := g.nodes[nodeID]
	if !ok {
		return "", false
	}
	return meta.GRPCAddr, true
}

// GetAllNodes returns all known nodes.
func (g *Gossip) GetAllNodes() []NodeMeta {
	g.mu.RLock()
	defer g.mu.RUnlock()
	nodes := make([]NodeMeta, 0, len(g.nodes))
	for _, meta := range g.nodes {
		nodes = append(nodes, meta)
	}
	return nodes
}

// NumMembers returns the number of members in the cluster.
func (g *Gossip) NumMembers() int {
	return g.memberlist.NumMembers()
}

// Leave gracefully leaves the cluster.
func (g *Gossip) Leave(timeout time.Duration) error {
	return g.memberlist.Leave(timeout)
}

// Shutdown shuts down the gossip service.
func (g *Gossip) Shutdown() error {
	return g.memberlist.Shutdown()
}

// ParseHostPort parses a host:port string and returns the host and port separately.
func ParseHostPort(addr string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, err
	}
	return host, port, nil
}
