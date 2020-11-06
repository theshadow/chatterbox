package main

import (
	"fmt"
	"io"
	"sync"
	"time"

	"google.golang.org/grpc/metadata"

	"github.com/golang/protobuf/ptypes/timestamp"

	"github.com/theshadow/chatterbox"
)

type server struct {
	// Create a one-to-many map where a channel topic is mapped to one or more nicknames
	Topics map[string]map[string]struct{}

	// create a one-to-one map of the nickname to the channel used to send messages to the client
	Clients map[string]chan *chatterbox.Response

	// specifies how many goroutines are used for broadcasting events out to clients subscribed to a topic.
	// the size specifies how many active goroutines are used at a given time until all clients have been
	// updated.
	broadcastRoutineCount int

	// specifies what size the buffer will be for the clients broadcasting channel
	clientBufferSize int

	// these mutexes protect access to the Topics and Clients maps
	topicsMu, clientsMu sync.RWMutex
}

func NewServer(opts ...Options) (*server, error) {
	broadcastRoutineCount := defaultBroadcastRoutineCount
	clientBufferSize := defaultClientBufferSize

	if len(opts) > 0 {
		if opts[0].BroadcastRoutineCount <= 0 {
			return nil, fmt.Errorf("invalid option value for BroadcastRoutineCount, must be a non-zero value")
		}
		if opts[0].BroadcastRoutineCount != defaultBroadcastRoutineCount {
			broadcastRoutineCount = int(opts[0].BroadcastRoutineCount)
		}

		if opts[0].ClientBufferSize <= 0 {
			return nil, fmt.Errorf("invalid option value for ClientBufferSize, must be a non-zero value")
		}
		if opts[0].ClientBufferSize != defaultBroadcastRoutineCount {
			clientBufferSize = int(opts[0].ClientBufferSize)
		}
	}

	svc := &server{
		Topics:                make(map[string]map[string]struct{}),
		Clients:               make(map[string]chan *chatterbox.Response),
		broadcastRoutineCount: broadcastRoutineCount,
		clientBufferSize:      clientBufferSize,
	}

	// the server topic always exists
	svc.Topics[ServerTopic] = make(map[string]struct{})

	return svc, nil
}

func (s *server) logf(format string, args ...interface{}) {
	fmt.Printf("[%s]:"+format, append([]interface{}{time.Now()}, args...)...)
}

func (s *server) Stream(stream chatterbox.Chatterbox_StreamServer) error {
	var nickname string

	// extract the client nickname from the metadata
	md, ok := metadata.FromIncomingContext(stream.Context())
	if !ok {
		// TODO return an error about not being able to read the metadata
	} else {
		res := md.Get(chatterbox.MetaDataProtocolVersion)
		if len(res) == 0 || len(res[0]) == 0 {
			// TODO return an error about missing protocol version
		} else if res[0] != chatterbox.ProtocolVersion {
			// TODO return an error about incompatible protocol versions
		}

		res = md.Get(chatterbox.MetaDataNickname)
		if len(res) == 0 || len(res[0]) == 0 {
			// TODO return an error about the missing or invalid nickname
		}
		nickname = res[0]
	}

	// check if the client already has a server topic subscription, if not create it and force the client to join the
	// topic
	s.clientsMu.Lock()
	if ch, ok := s.Clients[nickname]; !ok {
		// create the channel for the client to broadcast messages to
		s.Clients[nickname] = make(chan *chatterbox.Response, s.clientBufferSize)

		// force the client to join the "server" topic
		s.topicsMu.Lock()
		s.Topics[ServerTopic][nickname] = struct{}{}
		s.topicsMu.Unlock()

		// start the client broadcaster
		go func(chan *chatterbox.Response) {
			// TODO add a QUIT command that closes the channel so that this can quit safely.
			for resp := range ch {
				if err := stream.Send(resp); err != nil {
					s.logf("unable to send message: %s", err)
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
					if err := s.onClientPart(req, nickname); err != nil {
						s.logf("client failed to part topic: %s", err)
						continue
					}
				case req.GetClientPing() != nil:
					if err := s.onClientPing(req, nickname); err != nil {
						s.logf("client failed to ping: %s", err)
						continue
					}
				case req.GetClientMessage() != nil:
					if err := s.onClientMessage(req, nickname); err != nil {
						s.logf("client failed to broadcast to topic: %s", err)
						continue
					}
				}
			}
		}
	}
}

func (s *server) onClientJoin(req *chatterbox.Request, nickname string) error {
	join := req.GetClientJoin()
	if len(join.Topic) == 0 {
		return fmt.Errorf("invalid topic, topic must be a non-zero length string")
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

func (s *server) onClientPart(req *chatterbox.Request, nickname string) error {
	part := req.GetClientPart()
	if len(part.Topic) == 0 {
		return fmt.Errorf("invalid topic, topic must be a non-zero length string")
	} else if part.Topic == ServerTopic {
		return fmt.Errorf("invalid topic, client may not part the server topic")
	}

	// create a slice of all of the nicks subscribed to the topic so that they can be notified of the new subscription
	s.topicsMu.RLock()
	var clients []string
	for client := range s.Topics[part.Topic] {
		if client == nickname {
			continue
		}
		clients = append(clients, client)
	}
	s.topicsMu.RUnlock()

	s.broadcastToClients(clients, &chatterbox.Response{
		Event: &chatterbox.Response_ClientPart{
			ClientPart: &chatterbox.Response_Part{
				Topic: part.Topic,
				Nick:  nickname,
			}}})

	// remove client from topic
	s.topicsMu.Lock()
	if _, ok := s.Topics[part.Topic]; ok {
		delete(s.Topics[part.Topic], nickname)
	}
	s.topicsMu.Unlock()

	return nil
}

func (s *server) onClientPing(req *chatterbox.Request, nickname string) error {
	ping := req.GetClientPing()

	s.broadcastToClients([]string{nickname}, &chatterbox.Response{
		Event: &chatterbox.Response_ClientPong{
			ClientPong: &chatterbox.Response_Pong{
				SentAt: ping.Timestamp,
				ReceivedAt: &timestamp.Timestamp{},
			}}})

	return nil
}


func (s *server) onClientMessage(req *chatterbox.Request, nickname string) error {
	msg := req.GetClientMessage()
	if len(msg.Topic) == 0 {
		return fmt.Errorf("invalid topic, topic must be a non-zero length string")
	} else if msg.Topic == ServerTopic {
		return fmt.Errorf("invalid topic, can't broadcast to the server topic")
	}

	// create a slice of all of the nicks subscribed to the topic so that they can be notified of the new subscription
	s.topicsMu.RLock()
	var clients []string
	for client := range s.Topics[msg.Topic] {
		if client == nickname {
			continue
		}
		clients = append(clients, client)
	}
	s.topicsMu.RUnlock()

	s.broadcastToClients(clients, &chatterbox.Response{
		Event: &chatterbox.Response_ClientMessage{
			ClientMessage: &chatterbox.Response_Message{
				Topic: msg.Topic,
				Nick: nickname,
				Text: msg.Text,
			}}})

	return nil
}

func (s *server) broadcastToClients(clients []string, resp *chatterbox.Response) {
	var wg sync.WaitGroup

	// loop over the clients to notify in chunks so that we don't spin up an unbounded number of goroutines.
	length := len(clients)
	var n int
	for i := 0; i < length-1; i += s.broadcastRoutineCount {
		n = min(i+s.broadcastRoutineCount, length)

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
				ch <- resp
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

// Options represents configuration options for the gRPC service
type Options struct {
	// ClientBufferSize specifies how large the channel buffer is for queuing up messages
	ClientBufferSize uint `json:"clientBufferSize"`

	// BroadcastRoutineCount defines how many goroutines can be active when broadcasting messages out to subscribers
	BroadcastRoutineCount uint `json:"broadcastRoutineCount"`
}

const ServerTopic = "server"

const (
	defaultBroadcastRoutineCount = 10
	defaultClientBufferSize      = 1000
)
