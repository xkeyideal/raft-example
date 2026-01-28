// Package resolver provides address resolution for leader forwarding.
// It supports both static (hardcoded map) and dynamic (gossip-based) resolution.
package resolver

import "sync"

// AddressResolver is an interface for resolving Raft node IDs to gRPC addresses.
type AddressResolver interface {
	// GetGRPCAddr returns the gRPC address for a given node ID.
	GetGRPCAddr(nodeID string) (grpcAddr string, ok bool)
	// SetGRPCAddr sets the gRPC address for a given node ID (for static resolver).
	SetGRPCAddr(nodeID, grpcAddr string)
	// RemoveAddr removes the address mapping for a given node ID.
	RemoveAddr(nodeID string)
}

// StaticResolver is a static address resolver using a hardcoded map.
type StaticResolver struct {
	mu    sync.RWMutex
	addrs map[string]string // nodeID -> grpc_addr
}

// NewStaticResolver creates a new static resolver with optional initial mappings.
func NewStaticResolver(initial map[string]string) *StaticResolver {
	addrs := make(map[string]string)
	for k, v := range initial {
		addrs[k] = v
	}
	return &StaticResolver{
		addrs: addrs,
	}
}

// GetGRPCAddr returns the gRPC address for a given node ID.
func (r *StaticResolver) GetGRPCAddr(nodeID string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	addr, ok := r.addrs[nodeID]
	return addr, ok
}

// SetGRPCAddr sets the gRPC address for a given node ID.
func (r *StaticResolver) SetGRPCAddr(nodeID, grpcAddr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addrs[nodeID] = grpcAddr
}

// RemoveAddr removes the address mapping for a given node ID.
func (r *StaticResolver) RemoveAddr(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.addrs, nodeID)
}

// GRPCAddrGetter is an interface for getting gRPC addresses.
type GRPCAddrGetter interface {
	GetGRPCAddr(nodeID string) (string, bool)
}

// GossipResolver wraps a gossip instance to implement AddressResolver.
type GossipResolver struct {
	getter GRPCAddrGetter
}

// NewGossipResolver creates a new gossip-based resolver.
func NewGossipResolver(getter GRPCAddrGetter) *GossipResolver {
	return &GossipResolver{getter: getter}
}

// GetGRPCAddr returns the gRPC address for a given node ID.
func (r *GossipResolver) GetGRPCAddr(nodeID string) (string, bool) {
	return r.getter.GetGRPCAddr(nodeID)
}

// SetGRPCAddr is a no-op for gossip resolver (addresses are managed by gossip).
func (r *GossipResolver) SetGRPCAddr(nodeID, grpcAddr string) {
	// No-op: gossip manages addresses automatically
}

// RemoveAddr is a no-op for gossip resolver (addresses are managed by gossip).
func (r *GossipResolver) RemoveAddr(nodeID string) {
	// No-op: gossip manages addresses automatically
}
