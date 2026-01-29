package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/hashicorp/raft"
	"github.com/iancoleman/strcase"
	"github.com/xkeyideal/raft-example/fsm"
	pb "github.com/xkeyideal/raft-example/proto"
	"github.com/xkeyideal/raft-example/resolver"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const (
	grpcConnectTimeout = 3 * time.Second
)

var (

	// connCache caches gRPC connections to leader nodes to avoid creating new connections for each request
	connCache   = make(map[string]*grpc.ClientConn)
	connCacheMu sync.RWMutex
)

type KV interface {
	Apply(ctx context.Context, key, val string) (uint64, error)
	Query(ctx context.Context, key []byte, consistent bool) (uint64, []byte, error)
	Stats() (map[string]any, error)
	GetLeader() (bool, raft.ServerAddress, error)
}

type GRPCService struct {
	pb.UnimplementedExampleServer
	fsm      *fsm.StateMachine
	kv       KV
	resolver resolver.AddressResolver
}

// NewGrpcService creates a new gRPC service with the default static resolver.
func NewGrpcService(fsm *fsm.StateMachine, kv KV, staticLookup map[string]string) *GRPCService {
	return NewGrpcServiceWithResolver(fsm, kv, resolver.NewStaticResolver(staticLookup))
}

// NewGrpcServiceWithResolver creates a new gRPC service with a custom address resolver.
func NewGrpcServiceWithResolver(fsm *fsm.StateMachine, kv KV, r resolver.AddressResolver) *GRPCService {
	return &GRPCService{
		fsm:      fsm,
		kv:       kv,
		resolver: r,
	}
}

func (r *GRPCService) Add(ctx context.Context, req *pb.AddRequest) (*pb.AddResponse, error) {
	done, resp, err := r.forwardRequestToLeader(ctx, "Add", req)
	if err != nil {
		return nil, err
	}

	if done {
		return resp.(*pb.AddResponse), nil
	}

	index, err := r.kv.Apply(ctx, req.Key, req.Val)
	if err != nil {
		return nil, err
	}

	return &pb.AddResponse{
		CommitIndex: index,
	}, nil
}

func (r *GRPCService) Get(ctx context.Context, req *pb.GetRequest) (*pb.GetResponse, error) {
	if req.Linearizable {
		done, resp, err := r.forwardRequestToLeader(ctx, "Get", req)
		if done {
			if err != nil {
				return nil, err
			}

			return resp.(*pb.GetResponse), nil
		}
	}

	index, val, err := r.kv.Query(ctx, []byte(req.Key), req.Linearizable)
	if err != nil {
		return nil, err
	}

	return &pb.GetResponse{
		Value:       string(val),
		ReadAtIndex: index,
	}, nil
}

func (r *GRPCService) Stat(ctx context.Context, req *pb.StatRequest) (*pb.StatResponse, error) {
	stats, err := r.kv.Stats()
	if err != nil {
		return nil, err
	}

	b, _ := json.MarshalIndent(stats, "", "    ")
	return &pb.StatResponse{Stats: string(b)}, nil
}

// forwardRequestToLeader is used to potentially forward an RPC request to a remote DC or
// to the local leader depending upon the request.
//
// Returns a bool of if forwarding was performed, as well as any error. If
// false is returned (with no error) it is assumed that the current server
// should handle the request.
func (r *GRPCService) forwardRequestToLeader(forwardCtx context.Context, method string, req protoreflect.ProtoMessage) (bool, protoreflect.ProtoMessage, error) {
	methods := pb.File_service_proto.Services().ByName("Example").Methods()

	// Look up the command as CamelCase and as-is (usually snake_case).
	m := methods.ByName(protoreflect.Name(method))
	if m == nil {
		m = methods.ByName(protoreflect.Name(strcase.ToCamel(method)))
	}
	if m == nil {
		return false, nil, fmt.Errorf("unknown command %q", method)
	}

	// Find the leader
	isLeader, leader, rpcErr := r.kv.GetLeader()

	// Handle the case we are the leader
	if isLeader {
		return false, nil, nil
	}

	if rpcErr != nil {
		return false, nil, rpcErr
	}

	grpcAddr, ok := r.resolver.GetGRPCAddr(string(leader))
	if !ok {
		return false, nil, fmt.Errorf("unknown leader gRPC address for raft addr %s", leader)
	}

	conn, err := getOrCreateConn(grpcAddr)
	if err != nil {
		return false, nil, err
	}

	resp := messageFromDescriptor(m.Output()).Interface()
	if err := conn.Invoke(forwardCtx, "/Example/"+string(m.Name()), req, resp); err != nil {
		return false, nil, err
	}

	return true, resp, nil
}

// getOrCreateConn returns a cached connection or creates a new one.
// It also handles connection health checking and cleanup of stale connections.
func getOrCreateConn(addr string) (*grpc.ClientConn, error) {
	connCacheMu.RLock()
	conn, ok := connCache[addr]
	connCacheMu.RUnlock()

	if ok {
		state := conn.GetState()
		// Connection is healthy
		if state == connectivity.Ready || state == connectivity.Idle {
			return conn, nil
		}
		// Connection is connecting, wait for it
		if state == connectivity.Connecting {
			return conn, nil
		}
		// Connection is in TransientFailure or Shutdown, need to recreate
	}

	connCacheMu.Lock()
	defer connCacheMu.Unlock()

	// Double check after acquiring write lock
	// Also clean up any failed connections while we have the lock
	if conn, ok := connCache[addr]; ok {
		state := conn.GetState()
		if state == connectivity.Ready || state == connectivity.Idle || state == connectivity.Connecting {
			return conn, nil
		}
		// Close and remove failed connection
		conn.Close()
		delete(connCache, addr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), grpcConnectTimeout)
	defer cancel()

	newConn, err := grpc.DialContext(ctx, addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}

	connCache[addr] = newConn
	return newConn, nil
}

// CloseAllConns closes all cached gRPC connections.
// This should be called during graceful shutdown.
func CloseAllConns() {
	connCacheMu.Lock()
	defer connCacheMu.Unlock()

	for addr, conn := range connCache {
		if conn != nil {
			conn.Close()
		}
		delete(connCache, addr)
	}
}

// messageFromDescriptor creates a new Message for a MessageDescriptor.
func messageFromDescriptor(d protoreflect.MessageDescriptor) protoreflect.Message {
	for _, m := range protoTypes {
		if m.ProtoReflect().Descriptor() == d {
			return m.ProtoReflect().New()
		}
	}
	panic(fmt.Errorf("unknown type %q; please add it to protoTypes", d.FullName()))
}

// There is no way to go from a protoreflect.MessageDescriptor to an instance of the message :(
var protoTypes = []protoreflect.ProtoMessage{
	&pb.AddRequest{},
	&pb.AddResponse{},
	&pb.GetRequest{},
	&pb.GetResponse{},
}

// WatchKeyBlocking demonstrates the use of abandon mechanism for blocking queries.
// This method blocks until the key's value changes or the state is abandoned due to restore.
//
// Example client usage:
//
//	var lastIndex uint64 = 0
//	for {
//	    result, err := client.WatchKeyBlocking(ctx, "my-key", lastIndex)
//	    if err != nil {
//	        log.Printf("Watch error: %v", err)
//	        time.Sleep(time.Second)
//	        continue
//	    }
//	    if result.Abandoned {
//	        log.Println("State restored, restarting watch from beginning")
//	        lastIndex = 0
//	        continue
//	    }
//	    log.Printf("Key changed at index %d: %s", result.CurrentIndex, result.Value)
//	    lastIndex = result.CurrentIndex
//	}
func (r *GRPCService) WatchKeyBlocking(ctx context.Context, key string, minIndex uint64) (*BlockingQueryResult, error) {
	queryFn := func() ([]byte, uint64, error) {
		index, val, err := r.kv.Query(ctx, []byte(key), false)
		if err != nil {
			return nil, 0, err
		}
		return val, index, nil
	}

	return BlockingQuery(ctx, r.fsm, minIndex, 30*time.Second, queryFn)
}
