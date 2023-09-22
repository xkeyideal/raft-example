package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/hashicorp/raft"
	"github.com/iancoleman/strcase"
	"github.com/xkeyideal/raft-example/fsm"
	pb "github.com/xkeyideal/raft-example/proto"
	"github.com/xkeyideal/raft-example/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/reflect/protoreflect"
)

var _ service.KV = &raftServer{}

var (
	server_lookup = map[string]string{
		"127.0.0.1:50051": "127.0.0.1:40051",
		"127.0.0.1:50052": "127.0.0.1:40052",
		"127.0.0.1:50053": "127.0.0.1:40051",
	}
)

func (s *raftServer) Apply(key, val string, timeout time.Duration) (uint64, error) {
	done, resp, err := s.forwardRequestToLeader("Add", []string{key, val}...)
	if err != nil {
		return 0, err
	}

	if done {
		return resp.(*pb.AddResponse).CommitIndex, nil
	}

	if err := s.ConsistentRead(); err != nil {
		return 0, fmt.Errorf("ConsistentRead %s: %s", s.raftAddr, err.Error())
	}

	payload := fsm.SetPayload{
		Key:   key,
		Value: val,
	}

	b, _ := json.Marshal(payload)

	f := s.raft.Apply(b, timeout)
	if err := f.Error(); err != nil {
		return 0, fmt.Errorf("Apply %s: %s", s.raftAddr, err.Error())
	}

	return f.Index(), nil
}

func (s *raftServer) Query(key []byte, consistent bool, timeout time.Duration) (uint64, []byte, error) {
	if consistent {
		done, resp, err := s.forwardRequestToLeader("Get", []string{string(key)}...)
		if done {
			if err != nil {
				return 0, nil, err
			}

			r := resp.(*pb.GetResponse)

			return r.ReadAtIndex, []byte(r.Value), nil
		}

		if err := s.ConsistentRead(); err != nil {
			return 0, nil, fmt.Errorf("ConsistentRead %s: %s", s.raftAddr, err.Error())
		}
	}

	val, err := s.store.Lookup(key)
	if err != nil {
		return 0, nil, fmt.Errorf("Lookup %s: %s", s.raftAddr, err.Error())
	}

	return s.raft.AppliedIndex(), val, nil
}

// forwardRequestToLeader is used to potentially forward an RPC request to a remote DC or
// to the local leader depending upon the request.
//
// Returns a bool of if forwarding was performed, as well as any error. If
// false is returned (with no error) it is assumed that the current server
// should handle the request.
func (s *raftServer) forwardRequestToLeader(method string, params ...string) (bool, protoreflect.ProtoMessage, error) {
	methods := pb.File_service_proto.Services().ByName("Example").Methods()

	// Look up the command as CamelCase and as-is (usually snake_case).
	m := methods.ByName(protoreflect.Name(method))
	if m == nil {
		m = methods.ByName(protoreflect.Name(strcase.ToCamel(method)))
	}
	if m == nil {
		return false, nil, fmt.Errorf("unknown command %q", method)
	}

	// Sort fields by field number.
	reqDesc := m.Input()
	unorderedFields := reqDesc.Fields()
	fields := make([]protoreflect.FieldDescriptor, unorderedFields.Len())
	for i := 0; unorderedFields.Len() > i; i++ {
		f := unorderedFields.Get(i)
		fields[f.Number()-1] = f
	}

	// Convert given strings to the right type and set them on the request proto.
	req := messageFromDescriptor(reqDesc)
	for i, f := range fields {
		s := params[i] // parameters
		var v protoreflect.Value
		switch f.Kind() {
		case protoreflect.StringKind:
			v = protoreflect.ValueOfString(s)
		case protoreflect.BytesKind:
			v = protoreflect.ValueOfBytes([]byte(s))
		case protoreflect.Uint64Kind:
			i, err := strconv.ParseUint(s, 10, 64)
			if err != nil {
				return false, nil, err
			}
			v = protoreflect.ValueOfUint64(uint64(i))
		default:
			return false, nil, fmt.Errorf("internal error: kind %s is not yet supported", f.Kind().String())
		}
		req.Set(f, v)
	}

	// Find the leader
	isLeader, leader, rpcErr := s.getLeader()

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
	if err := conn.Invoke(ctx, "/Example/"+string(m.Name()), req.Interface(), resp); err != nil {
		return false, nil, err
	}

	return true, resp, nil
}

func (s *raftServer) getLeader() (bool, raft.ServerAddress, error) {
	// Check if we are the leader
	if s.IsLeader() {
		return true, "", nil
	}

	// Get the leader
	leader := s.raft.Leader()
	if leader == "" {
		return false, "", ErrNoLeader
	}

	return false, leader, nil
}

// IsLeader checks if this server is the cluster leader
func (s *raftServer) IsLeader() bool {
	return s.raft.State() == raft.Leader
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
