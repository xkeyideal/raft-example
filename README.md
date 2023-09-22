# raft-example

This is some example code for how to use [Hashicorp's Raft implementation](https://github.com/hashicorp/raft) with gRPC.

## Start your own cluster

This example uses Hashicorp's Raft Transport to communicate between nodes using TCP.

This example use two independent ports support application's service and raft's transport.

GRPC Server port: 40051, 40052, 40053, 40054 ...
RAFT Server port: 50051, 50052, 50053, 50054 ...

```shell
$ mkdir /Users/xkey/test
$ mkdir /Users/xkey/test/node{A,B,C}
$ ./raft-example --raft_bootstrap --raft_id=nodeA --grpc_addr=localhost:40051 --raft_addr=localhost:50051 --raft_data_dir /Users/xkey/test
$ ./raft-example --raft_id=nodeB --grpc_addr=localhost:40052 --raft_addr=localhost:50052 --raft_data_dir /Users/xkey/test
$ ./raft-example --raft_id=nodeC --grpc_addr=localhost:40053 --raft_addr=localhost:50053 --raft_data_dir /Users/xkey/test

$ go install github.com/xkeyideal/raft-manager/cmd/manager@latest
$ ./manager localhost:40051 add_voter nodeB localhost:50052 0
$ ./manager localhost:40051 add_voter nodeC localhost:50053 0
$ go run cmd/cmd.go &
$ ./manager localhost:40051 leadership_transfer
```

You start up three nodes, and bootstrap one of them. Then you tell the bootstrapped node where to find peers. Those peers sync up to the state of the bootstrapped node and become members of the cluster. Once your cluster is running, you never need to pass `--raft_bootstrap` again.

[raft-manager](https://github.com/xkeyideal/raft-manager) is used to communicate with the cluster and add the other nodes.

## Raft

Raft uses logs to synchronize changes. Every change submitted to a Raft cluster is a log entry, which gets stored and replicated to the followers in the cluster. In this example, we use [raft-pebbledb](https://github.com/xkeyideal/raft-pebbledb) to store these logs.
Once in a while Raft decides the logs have grown too large, and makes a snapshot. Your code is asked to write out its state. That state captures all previous logs. Now Raft can delete all the old logs and just use the snapshot. These snapshots are stored using the FileSnapshotStore, which means they'll just be files in your disk.

You can see all this happening in `NewRaft()` in `engine/raft.go`.

## Your FSM

See `fsm/fsm.go`. You'll need to implement a `raft.FSM`, and you probably want a gRPC RPC interface.

## Consistent

The `Apply` method of Hashicorp Raft must be called by leader.

```go
// Apply is used to apply a command to the FSM in a highly consistent
// manner. This returns a future that can be used to wait on the application.
// An optional timeout can be provided to limit the amount of time we wait
// for the command to be started. This must be run on the leader or it
// will fail.
func (r *Raft) Apply(cmd []byte, timeout time.Duration) ApplyFuture {
	return r.ApplyLog(Log{Data: cmd}, timeout)
}
```

So we need forward `Apply` request to the leader. Like the [Consul](https://github.com/hashicorp/consul) KV https://github.com/hashicorp/consul/blob/main/agent/consul/kvs_endpoint.go#L100 by RPC.

We must know the leader RPC address not raft address when want to forward request to leader.

We can use gossip protocol propagate per node RPC, raft address infos and so on. Best practices of [Gossip](https://github.com/xkeyideal/mraft/blob/master/gossip/gossip.go) by [Memberlist](https://github.com/hashicorp/memberlist)

The Raft Example use constant map, you can modify it in https://github.com/xkeyideal/raft-example/blob/master/engine/kv.go

```go
var (
	server_lookup = map[string]string{
		"127.0.0.1:50051": "127.0.0.1:40051",
		"127.0.0.1:50052": "127.0.0.1:40052",
		"127.0.0.1:50053": "127.0.0.1:40051",
	}
)
```



## Inspired

[raft-grpc-example](https://github.com/Jille/raft-grpc-example)

## License
raft-example is under the BSD 2-Clause License. See the LICENSE file for details.