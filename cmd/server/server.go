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

	topicsMu, clientsMu sync.RWMutex
}

func NewServer(host, password string) *server {
	svc := &server{
		Host: host,
		Password: password,
		Topics: make(map[string]map[string]struct{}),
		Clients: make(map[string]chan *chatterbox.Response),
	}

	// the server topic always exists
	svc.Topics[ServerTopic] = make(map[string]struct{})

	return svc
}

func (s *server) logf(format string, args ...interface{}) {
	fmt.Printf("[%s]:" + format, append([]interface{}{time.Now()}, args...)...)
}

func (s *server) Stream(stream chatterbox.Chatterbox_StreamServer) error {
	var clientID string

	// extract the client ID from the metadata
	md, ok := metadata.FromIncomingContext(stream.Context())
	if !ok {
		// TODO error
	} else {
		res := md.Get(chatterbox.MetaDataClientId)
		if len(res) == 0 || len(res[0]) == 0{
			// TODO error
		}
		clientID = res[0]
	}

	// check if the client already has a channel, if not create it and force the client to join the server topic
	s.clientsMu.Lock()
	if ch, ok := s.Clients[clientID]; !ok {
		// create the channel for the client to broadcast messages to
		// TODO make the buffer size variable
		s.Clients[clientID] = make(chan *chatterbox.Response, 1000)

		// force the client to join the "server" topic
		s.topicsMu.Lock()
		s.Topics[ServerTopic][clientID] = struct{}{}
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
		// TODO this client already exists, are they trying to shadow another clientID???
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
						join := req.GetClientJoin()
						if len(join.Topic) == 0 {
							// TODO return proper gRPC error
							return nil
						}

						// add client to topic
						s.topicsMu.Lock()
						if _, ok := s.Topics[join.Topic]; !ok {
							// the requested topic does not exist, so create it and add the user
							s.Topics[join.Topic] = make(map[string]struct{})
						} else {
							// the topic exists add the client to the list of joined clients for the topic
							s.Topics[join.Topic][clientID] = struct{}{}
						}
						s.topicsMu.Unlock()

						var wg sync.WaitGroup

						// notify all other clients that user joined topic
						s.topicsMu.RLock()
						for client := range s.Topics[join.Topic] {
							// grab the channel for the client, if it doesn't exist skip it.
							// it may not exist by the time we get here if the client sent a part/quit event.
							s.clientsMu.RLock()
							ch, ok := s.Clients[client]
							if !ok {
								continue
							}
							s.clientsMu.RUnlock()

							// create a message writer for each of the channels
							wg.Add(1)
							// lock the clients because we'll be writing to them.
							go func(topic, clientID string, ch chan *chatterbox.Response) {
								defer wg.Done()
								ch <-&chatterbox.Response{
									Event: &chatterbox.Response_ClientJoin{
										ClientJoin: &chatterbox.Response_Join{
											Topic: topic,
											Nick: clientID,
										},
									},
								}
							}(join.Topic, clientID, ch)

							wg.Wait()
						}
						s.topicsMu.RUnlock()
						break
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

// TODO make sure all clients join the server topic by default
const ServerTopic = "server"

