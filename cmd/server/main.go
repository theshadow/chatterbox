package main

import (
	"log"
	"net"

	"google.golang.org/grpc"

	"github.com/theshadow/chatterbox"
)

var (
	Version string
	BuildRef string
)

func main() {
	conn, err := net.Listen("tcp", ":50051")
	if err != nil {
		log.Fatalf("unable to listen on :50051: %v", err)
	}

	grpcSvc := grpc.NewServer()
	if svc, err := NewServer(); err != nil {
		log.Fatalf("unable to instantiate gRPC service: %s", err)
	} else {
		chatterbox.RegisterChatterboxServer(grpcSvc, svc)
		if err := grpcSvc.Serve(conn); err != nil {
			log.Fatalf("unable to serve: %v", err)
		}
	}
}
