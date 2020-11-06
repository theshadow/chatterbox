package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"chatterbox"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	go func(signals chan os.Signal, cancel context.CancelFunc) {
		<-signals
		signal.Stop(signals)
		close(signals)
		cancel()
	}(signals, cancel)

	connCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	conn, err := grpc.DialContext(connCtx, ":50051", grpc.WithInsecure(), grpc.WithBlock())
	if err != nil {
		log.Fatalf("unable to dial host: %s", err)
	}
	defer conn.Close()

	client := NewClient("bonzo")
	client.ChatterboxClient = chatterbox.NewChatterboxClient(conn)

	if err := client.stream(ctx); err != nil {
		log.Fatalf("stream failed: %s", err)
	}
}
