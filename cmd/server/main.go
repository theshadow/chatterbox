package main

import (
	"log"
	"net"

	"google.golang.org/grpc"

	"chatterbox"
)


func main() {

	conn, err := net.Listen("tcp", ":50051")
	if err != nil {
		log.Fatalf("unable to listen on :50051: %v", err)
	}

	s := grpc.NewServer()
	chatterbox.RegisterChatServerServer(s, NewServer())
	if err := s.Serve(conn); err != nil {
		log.Fatalf("unable to serve: %v", err)
	}
}
