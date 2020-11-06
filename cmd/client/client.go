package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/theshadow/chatterbox"
)

type client struct {
	chatterbox.ChatterboxClient
	Nickname string
}

func NewClient(nickname string) *client {
	return &client{
		Nickname: nickname,
	}
}

func (c *client) stream(ctx context.Context) error {
	md := metadata.New(map[string]string{
		chatterbox.MetaDataProtocolVersion: chatterbox.ProtocolVersion,
		chatterbox.MetaDataNickname: c.Nickname})

	ctx = metadata.NewOutgoingContext(ctx, md)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	client, err := c.ChatterboxClient.Stream(ctx)
	if err != nil {
		return err
	}
	defer client.CloseSend()

	go c.send(client)

	return c.receive(client)
}

func (c *client) receive(stream chatterbox.Chatterbox_StreamClient) error {
	for {
		res, err := stream.Recv()

		if s, ok := status.FromError(err); ok && s.Code() == codes.Canceled {
			c.logf("stream shutdown due to cancellation")
			break
		} else if err == io.EOF {
			c.logf("stream closed by remote host")
			break
		} else if err != nil {
			return err
		}

		switch event := res.Event.(type) {
		case *chatterbox.Response_ClientJoin:
			c.logf("Nick: %s joined topic: %s", event.ClientJoin.Nick, event.ClientJoin.Topic)
		case *chatterbox.Response_ClientPart:
			c.logf("Nick: %s parted topic: %s", event.ClientPart.Nick, event.ClientPart.Topic)
		case *chatterbox.Response_ClientPong:
			c.logf("Ping: %s Pong: %s", event.ClientPong.SentAt, event.ClientPong.ReceivedAt)
		case *chatterbox.Response_ClientMessage:
			c.logf("|%s| %s: %s", event.ClientMessage.Topic, event.ClientMessage.Nick, event.ClientMessage.Text)
		default:
			break
		}
	}
	return nil
}

func (c *client) send(stream chatterbox.Chatterbox_StreamClient) {
	sc := bufio.NewScanner(os.Stdin)
	sc.Split(bufio.ScanLines)

	for {
		select {
		case <-stream.Context().Done():
			c.logf("client send loop disconnected")
		default:
			if sc.Scan() {
				txt := sc.Text()
				if !strings.HasPrefix(txt, "/") {
					c.logf("unknown command: %s", txt)
					continue
				}

				// TODO refactor this switch into multiple functions for clarity
				parts := strings.Split(txt, " ")
				switch parts[0][1:] {
				case "join":
					if len(parts) < 2 {
						c.logf("invalid join command, must be of the form /join <TOPIC>")
						continue
					}
					err := stream.Send(&chatterbox.Request{
						Event: &chatterbox.Request_ClientJoin{
							ClientJoin: &chatterbox.Request_Join{
								Topic: parts[1],
							},
						},
					})
					if err != nil {
						c.logf("unable to join topic: %s", err)
						continue
					}
				case "part":
					if len(parts) < 2 {
						c.logf("invalid join command, must be of the form /part <TOPIC>")
						continue
					}
					err := stream.Send(&chatterbox.Request{
						Event: &chatterbox.Request_ClientPart{
							ClientPart: &chatterbox.Request_Part{
								Topic: parts[1],
							},
						},
					})
					if err != nil {
						c.logf("unable to part topic: %s", err)
						continue
					}
				case "ping":
					err := stream.Send(&chatterbox.Request{
						Event: &chatterbox.Request_ClientPing{
							ClientPing: &chatterbox.Request_Ping{},
						},
					})
					if err != nil {
						c.logf("unable to send ping: %s", err)
						continue
					}
				case "msg":
					if len(parts) < 3 {
						c.logf("invalid join command, must be of the form /msg <TOPIC> <MESSAGE>")
						continue
					}
					err := stream.Send(&chatterbox.Request{
						Event: &chatterbox.Request_ClientMessage{
							ClientMessage: &chatterbox.Request_Message{
								Topic: parts[1],
								Text: strings.Join(parts[2:], ""),
							},
						},
					})
					if err != nil {
						c.logf("unable to send message: %s", err)
						continue
					}
				default:
					c.logf("unknown command %s", parts[0][1:])
				}
				err := stream.Send(&chatterbox.Request{
					Event: &chatterbox.Request_ClientMessage{
						ClientMessage: &chatterbox.Request_Message{
							Topic: "home",
							Text: sc.Text(),
						},
					},
				})
				if err != nil {
					c.logf("failed to send message: %v", err)
					return
				}
			} else {
				c.logf("input scanner failure: %v", sc.Err())
				return
			}
		}
	}
}

func (c *client) logf(format string, args ...interface{}) {
	fmt.Printf("[%s] "+format, append([]interface{}{time.Now()}, args...)...)
}

