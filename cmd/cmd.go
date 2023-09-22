// Binary hammer sends requests to your Raft cluster as fast as it can.
// It sends the written out version of the Dutch numbers up to 2000.
// In the end it asks the Raft cluster what the longest three words were.
package main

import (
	"context"
	"crypto/rand"
	"io"
	"log"
	"time"

	"github.com/xkeyideal/grpcbalance/grpclient"
	"github.com/xkeyideal/grpcbalance/grpclient/balancer"
	pb "github.com/xkeyideal/raft-example/proto"
)

func main() {
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
		log.Fatalf("dialing failed: %v", err)
	}
	defer client.Close()

	c := pb.NewExampleClient(client.ActiveConnection())

	keys := []string{}
	for i := 0; i < 10; i++ {
		key := randomId(10)
		val := randomId(32)
		keys = append(keys, key)
		resp, err := c.Add(context.Background(), &pb.AddRequest{Key: key, Val: val})
		if err != nil {
			log.Fatalf("AddWord RPC failed: %v", err)
		}

		log.Println("Apply:", key, val, resp.CommitIndex)
	}

	time.Sleep(2 * time.Second)

	for _, key := range keys {
		resp, err := c.Get(context.Background(), &pb.GetRequest{
			Key:          key,
			Linearizable: false,
		})
		if err != nil {
			log.Fatalf("GetWords RPC failed: %v", err)
		}

		log.Println("Query:", key, "==>", resp.Value, resp.ReadAtIndex)
	}
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
