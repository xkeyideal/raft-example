// Binary hammer sends requests to your Raft cluster as fast as it can.
// It sends the written out version of the Dutch numbers up to 2000.
// In the end it asks the Raft cluster what the longest three words were.
package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"sync"
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

	ch := generateWords()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for w := range ch {
				_, err := c.AddWord(context.Background(), &pb.AddWordRequest{Word: w})
				if err != nil {
					log.Fatalf("AddWord RPC failed: %v", err)
				}
			}
		}()
	}
	wg.Wait()

	resp, err := c.GetWords(context.Background(), &pb.GetWordsRequest{})
	if err != nil {
		log.Fatalf("GetWords RPC failed: %v", err)
	}
	fmt.Println(resp)
}

func generateWords() <-chan string {
	ch := make(chan string, 1)
	go func() {
		for {
			ch <- randomId()
		}
	}()

	return ch
}

var idChars = []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789")

const idLen = 20

func randomId() string {
	b := randomBytesMod(idLen, byte(len(idChars)))
	for i, c := range b {
		b[i] = idChars[c]
	}
	return string(b)
}

func randomCity() string {
	b := randomBytesMod(4, byte(len(idChars)))
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
