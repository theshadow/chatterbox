package main

import (
	"fmt"
	"io"
	"sync"
	"time"

	"google.golang.org/grpc/metadata"

	"chatterbox"
)

type server struct {
	Host, Password string

	// Create a one-to-many map where a channel topic is mapped to one or more client IDs
	Topics map[string]map[string]struct{}

	// create a one-to-one map of the client ID to the channel
	Clients map[string]chan *chatterbox.Response

	// specifies how many goroutines are used for broadcasting events out to clients subscribed to a topic.
	// the size specifies how many active goroutines are used at a given time until all clients have been
	// updated.
	broadcastChunkSize int

	// specifies what size the buffer will be for the clients broadcasting channel
	broadcastBufferSize int

	topicsMu, clientsMu sync.RWMutex
}

func NewServer(host, password string) *server {
	svc := &server{
		Host: host,
		Password: password,
		Topics: make(map[string]map[string]struct{}),
		Clients: make(map[string]chan *chatterbox.Response),
		broadcastChunkSize: 10,
		broadcastBufferSize: 1000,
	}

	// the server topic always exists
	svc.Topics[ServerTopic] = make(map[string]struct{})

	return svc
}

func (s *server) logf(format string, args ...interface{}) {
	fmt.Printf("[%s]:" + format, append([]interface{}{time.Now()}, args...)...)
}

func (s *server) Stream(stream chatterbox.Chatterbox_StreamServer) error {
	var nickname string

	// extract the client nickname from the metadata
	md, ok := metadata.FromIncomingContext(stream.Context())
	if !ok {
		// TODO error
	} else {
		res := md.Get(chatterbox.MetaDataNickname)
		if len(res) == 0 || len(res[0]) == 0 {
			// TODO error
		}
		nickname = res[0]
	}

	// check if the client already has a server topic subscription, if not create it and force the client to join the
	// topic
	s.clientsMu.Lock()
	if ch, ok := s.Clients[nickname]; !ok {
		// create the channel for the client to broadcast messages to
		s.Clients[nickname] = make(chan *chatterbox.Response, s.broadcastBufferSize)

		// force the client to join the "server" topic
		s.topicsMu.Lock()
		s.Topics[ServerTopic][nickname] = struct{}{}
		s.topicsMu.Unlock()

		// start the client broadcaster
		go func(chan *chatterbox.Response) {
			// TODO add a QUIT command that closes the channel so that this can quit safely.
			for resp := range ch {
				if err := stream.Send(resp); err != nil {
					// TODO return error about unable to send message
				}
			}
		}(ch)
	} else {
		// TODO this client already exists, are they trying to shadow another nickname???
	}
	s.clientsMu.Unlock()

	for {
		select {
			case <-stream.Context().Done():
				// TODO should I close the client channel here?
				return stream.Context().Err()
			default:
				// TODO return proper gRPC status errors
				if req, err := stream.Recv(); err == io.EOF {
					s.logf("received EOF")
				} else if err != nil {
					return err
				} else {
					switch {
					case req.GetClientJoin() != nil:
						if err := s.onClientJoin(req, nickname); err != nil {
							s.logf("client failed to join topic: %s", err)
							continue
						}
					case req.GetClientPart() != nil:
						break
					case req.GetClientPing() != nil:
						break
					case req.GetClientMessage() != nil:
						break
					}
				}
		}
	}
}

func (s *server) onClientJoin(req *chatterbox.Request, nickname string) error {
	join := req.GetClientJoin()
	if len(join.Topic) == 0 {
		// TODO return proper gRPC error
		return nil
	}

	// add client to topic
	s.topicsMu.Lock()
	if _, ok := s.Topics[join.Topic]; !ok {
		// the requested topic does not exist, so create it.
		s.Topics[join.Topic] = make(map[string]struct{})
	}
	// the topic exists add the client to the list of joined clients for the topic
	s.Topics[join.Topic][nickname] = struct{}{}
	s.topicsMu.Unlock()

	// create a slice of all of the nicks subscribed to the topic so that they can be notified of the new subscription
	s.topicsMu.RLock()
	var clients []string
	for client := range s.Topics[join.Topic] {
		if client == nickname {
			continue
		}
		clients = append(clients, client)
	}
	s.topicsMu.RUnlock()

	s.broadcastToClients(clients, &chatterbox.Response{
		Event: &chatterbox.Response_ClientJoin{
			ClientJoin: &chatterbox.Response_Join{
				Topic: join.Topic,
				Nick:  nickname,
			}}})

	return nil
}

func (s *server) broadcastToClients(clients []string, resp *chatterbox.Response) {
	var wg sync.WaitGroup

	// loop over the clients to notify in chunks so that we don't spin up an unbounded number of goroutines.
	length := len(clients)
	var n int
	for i := 0; i < length-1; i += s.broadcastChunkSize {
		n = min(i+s.broadcastChunkSize, length)

		for _, client := range clients[i:n] {
			// grab the broadcasting channel for the client, if it doesn't exist skip it.
			// it may not exist by the time we get here if the client sent a part/quit event.
			s.clientsMu.RLock()
			ch, ok := s.Clients[client]
			if !ok {
				continue
			}
			s.clientsMu.RUnlock()

			// create a response pump that sends out the response.
			wg.Add(1)
			go func(ch chan *chatterbox.Response, resp *chatterbox.Response) {
				defer wg.Done()
				ch <-resp
			}(ch, resp)
		}

		wg.Wait()
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TODO make sure all clients join the server topic by default
const ServerTopic = "server"

