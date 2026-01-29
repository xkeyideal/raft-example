// Binary hammer sends requests to your Raft cluster as fast as it can.
// It sends the written out version of the Dutch numbers up to 2000.
// In the end it asks the Raft cluster what the longest three words were.
package main

import (
	"context"
	"crypto/rand"
	"flag"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/xkeyideal/grpcbalance/grpclient"
	"github.com/xkeyideal/grpcbalance/grpclient/balancer"
	pb "github.com/xkeyideal/raft-example/proto"
)

var (
	watchMode  = flag.Bool("watch", false, "Run in watch mode to demonstrate abandon mechanism")
	keyToWatch = flag.String("key", "test-watch-key", "Key to watch in watch mode")
)

func main() {
	flag.Parse()

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

	if *watchMode {
		runWatchDemo(c)
		return
	}

	runKVDemo(c)
}

// runKVDemo demonstrates basic KV operations
func runKVDemo(c pb.ExampleClient) {
	keys := []string{}
	for i := 0; i < 5; i++ {
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

	log.Println("================== KV WRITE TEST DONE ==================")

	for i, key := range keys {
		linearizable := false
		if i%3 == 0 {
			linearizable = true
		}

		resp, err := c.Get(context.Background(), &pb.GetRequest{
			Key:          key,
			Linearizable: linearizable,
		})
		if err != nil {
			log.Fatalf("GetWords RPC failed: %v", err)
		}

		log.Println("Query:", linearizable, key, "==>", resp.Value, resp.ReadAtIndex)
	}

	log.Println("================== KV READ TEST DONE ==================")

	stats, err := c.Stat(context.Background(), &pb.StatRequest{})
	if err != nil {
		log.Fatalf("Stat RPC failed: %v", err)
	}

	log.Println(stats.Stats)
}

// runWatchDemo demonstrates the Watch RPC with abandon mechanism.
// This is a Consul-style blocking query that:
// 1. Waits for changes to a key
// 2. Gets notified when state is abandoned (snapshot restore)
// 3. Automatically reconnects and continues watching
//
// Usage:
//
//	# Terminal 1: Start watching a key
//	go run cmd/cmd.go -watch -key=my-key
//
//	# Terminal 2: Write to the key to see watch updates
//	# Use another client or manager to write values
func runWatchDemo(c pb.ExampleClient) {
	log.Printf("Starting Watch demo for key: %s", *keyToWatch)
	log.Println("Press Ctrl+C to stop")
	log.Println()

	// First, write an initial value to the key
	log.Println("Writing initial value...")
	resp, err := c.Add(context.Background(), &pb.AddRequest{
		Key: *keyToWatch,
		Val: "initial-value-" + randomId(8),
	})
	if err != nil {
		log.Fatalf("Failed to write initial value: %v", err)
	}
	log.Printf("Initial value written at index: %d", resp.CommitIndex)
	log.Println()

	// Start background writer to simulate changes
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("\nShutting down...")
		cancel()
	}()

	// Start a goroutine to periodically update the key
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				newVal := "updated-" + randomId(8)
				_, err := c.Add(ctx, &pb.AddRequest{
					Key: *keyToWatch,
					Val: newVal,
				})
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					log.Printf("Background writer error: %v", err)
					continue
				}
				log.Printf("[Writer] Updated key to: %s", newVal)
			}
		}
	}()

	// Watch the key using gRPC streaming
	log.Println("Starting to watch key...")
	log.Println("(Updates will appear every 5 seconds from background writer)")
	log.Println()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := doWatchKey(ctx, c, *keyToWatch); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("Watch error: %v, retrying in 1 second...", err)
			time.Sleep(time.Second)
		}
	}
}

// doWatchKey demonstrates the Watch RPC client implementation.
// It handles both normal updates and abandon events.
func doWatchKey(ctx context.Context, c pb.ExampleClient, key string) error {
	stream, err := c.Watch(ctx, &pb.WatchRequest{
		Key:            key,
		MinIndex:       0, // Start from beginning
		TimeoutSeconds: 30,
	})
	if err != nil {
		return err
	}

	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			log.Println("Watch stream closed by server")
			return nil
		}
		if err != nil {
			return err
		}

		if resp.Abandoned {
			// State was abandoned due to snapshot restore
			// This is the KEY feature of the abandon mechanism!
			log.Println()
			log.Println("!!! STATE ABANDONED !!!")
			log.Println("A snapshot restore occurred. Need to reset and re-query.")
			log.Println("This is critical for maintaining data consistency in blocking queries.")
			log.Println()
			// Reconnect from the beginning (minIndex=0)
			return nil
		}

		// Normal update
		log.Printf("[Watch] Index=%d, Value=%s", resp.CurrentIndex, resp.Value)
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
