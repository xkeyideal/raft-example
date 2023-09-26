package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/hashicorp/raft"
	"github.com/iancoleman/strcase"
	"github.com/xkeyideal/raft-example/fsm"
	pb "github.com/xkeyideal/raft-example/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const (
	grpcConnectTimeout = 3 * time.Second
)

var (
	server_lookup = map[string]string{
		"127.0.0.1:50051": "127.0.0.1:40051",
		"127.0.0.1:50052": "127.0.0.1:40052",
		"127.0.0.1:50053": "127.0.0.1:40051",
	}
)

func ctxTimeout(ctx context.Context) time.Duration {
	deadline, _ := ctx.Deadline()
	timeout := time.Until(deadline)
	if timeout <= 0 {
		return 0
	}
	return timeout
}

type KV interface {
	Apply(ctx context.Context, key, val string) (uint64, error)
	Query(ctx context.Context, key []byte, consistent bool) (uint64, []byte, error)
	Stats() (map[string]any, error)
	GetLeader() (bool, raft.ServerAddress, error)
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

	ctx, dcancel := context.WithTimeout(context.Background(), grpcConnectTimeout)
	defer dcancel()

	conn, err := grpc.DialContext(ctx, server_lookup[string(leader)],
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return false, nil, err
	}
	defer conn.Close()

	log.Printf("Request: %s", req)

	resp := messageFromDescriptor(m.Output()).Interface()
	if err := conn.Invoke(forwardCtx, "/Example/"+string(m.Name()), req, resp); err != nil {
		return false, nil, err
	}

	return true, resp, nil
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
