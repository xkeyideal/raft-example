package benchmark

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	mr "math/rand"
	"testing"
	"time"

	"github.com/xkeyideal/grpcbalance/grpclient"
	"github.com/xkeyideal/grpcbalance/grpclient/balancer"
	pb "github.com/xkeyideal/raft-example/proto"
)

func newClient() *grpclient.Client {
	cfg := &grpclient.Config{
		Endpoints:            []string{"localhost:40051", "localhost:40052", "localhost:40053"},
		BalanceName:          balancer.RoundRobinBalanceName,
		DialTimeout:          10 * time.Second,
		DialKeepAliveTime:    10 * time.Second,
		DialKeepAliveTimeout: 2 * time.Second,
		PermitWithoutStream:  true,
	}

	client, err := grpclient.NewClient(cfg)
	if err != nil {
		panic(err)
	}

	return client
}

func benchmarkRaftPut(parallel, size, vlen, start int, client *grpclient.Client, b *testing.B) {
	reqs := []*pb.AddRequest{}
	for i := 0; i < size; i++ {
		key := fmt.Sprintf("%d", start+i)
		val := randomId(vlen)
		reqs = append(reqs, &pb.AddRequest{Key: key, Val: val})
	}

	c := pb.NewExampleClient(client.ActiveConnection())

	sem := make(chan struct{}, parallel)

	st := time.Now()
	for _, req := range reqs {
		acquire(sem)
		go func(req *pb.AddRequest, sem chan struct{}, b *testing.B) {
			_, err := c.Add(context.Background(), req)
			if err != nil {
				panic(err)
			}
			release(sem)
		}(req, sem, b)
	}
	ed := time.Now()

	b.Logf("raft put parallel: %d, size: %d, bytes: %d, per write average time: %dns\n", parallel, size, vlen, int(ed.Sub(st))/size)
}

// go test -benchmem -run=^$ -bench ^Benchmark_RaftPut$
func Benchmark_RaftPut(b *testing.B) {
	client := newClient()

	// benchmark, 16 bytes, 100 kv pairs
	benchmarkRaftPut(4, 100, 16, 0, client, b)

	// benchmark, 32 bytes, 100 kv pairs
	benchmarkRaftPut(4, 100, 32, 100, client, b)

	// benchmark, 64 bytes, 100 kv pairs
	benchmarkRaftPut(4, 100, 64, 200, client, b)

	// benchmark, 128 bytes, 100 kv pairs
	benchmarkRaftPut(4, 100, 128, 300, client, b)

	// benchmark, 512 bytes, 100 kv pairs
	benchmarkRaftPut(4, 100, 512, 400, client, b)

	// benchmark, 1024 bytes, 100 kv pairs
	benchmarkRaftPut(4, 100, 1024, 500, client, b)

	//client.Close()
}

func benchmarkRaftGet(size int, client *grpclient.Client, b *testing.B) {
	keys := []string{}
	for i := 0; i < size; i++ {
		keys = append(keys, fmt.Sprintf("%d", mr.Intn(500)))
	}

	c := pb.NewExampleClient(client.ActiveConnection())

	st := time.Now()
	for i, key := range keys {
		linearizable := false
		if i%3 == 0 {
			linearizable = true
		}

		_, err := c.Get(context.Background(), &pb.GetRequest{
			Key:          key,
			Linearizable: linearizable,
		})
		if err != nil {
			panic(err)
		}
	}
	ed := time.Now()

	b.Logf("raft get size: %d, per read average time: %dns\n", size, int(ed.Sub(st))/size)
}

// go test -benchmem -run=^$ -bench ^Benchmark_RaftGet$
func Benchmark_RaftGet(b *testing.B) {
	client := newClient()

	benchmarkRaftGet(500, client, b)
}

func acquire(ch chan struct{}) {
	ch <- struct{}{}
}

func release(ch chan struct{}) {
	<-ch
}

var idChars = []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789")

func randomId(idLen int) string {
	b := randomBytesMod(idLen, byte(len(idChars)))
	for i, c := range b {
		b[i] = idChars[c]
	}
	return string(b)
}

func randomBytes(length int) (b []byte) {
	b = make([]byte, length)
	io.ReadFull(rand.Reader, b)
	return
}

func randomBytesMod(length int, mod byte) (b []byte) {
	maxrb := 255 - byte(256%int(mod))
	b = make([]byte, length)
	i := 0
	for {
		r := randomBytes(length + (length / 4))
		for _, c := range r {
			if c > maxrb {
				continue
			}
			b[i] = c % mod
			i++
			if i == length {
				return b
			}
		}
	}
}
