// Command base is the binary-size baseline: the shared core dialling wss://
// with an unauthenticated pass-through channel where a design would go. The
// mtls and noise commands differ from it only by the design.
package main

import (
	"context"
	"log"
	"os"

	"github.com/coder/websocket"

	"dash.cha.sh/spikes/node-transport/core"
)

func main() {
	n, err := core.NewNode(plain{}, os.Getenv("CHADASH_HUB"), "")
	if err != nil {
		log.Fatal(err)
	}
	n.Resume(os.Getenv("CHADASH_NODE_ID"))
	log.Fatal(n.Run(context.Background()))
}

type plain struct{}

func (plain) Handshake(_ context.Context, ws *websocket.Conn) (core.Channel, error) {
	return ch{ws}, nil
}
func (plain) Pins() []string                  { return nil }
func (plain) AddPin(string) error             { return nil }
func (plain) EnrollMaterial() ([]byte, error) { return nil, nil }
func (plain) Install([]byte) error            { return nil }
func (plain) BeginRotate() ([]byte, error)    { return nil, nil }
func (plain) CommitRotate([]byte) error       { return nil }

type ch struct{ ws *websocket.Conn }

func (c ch) Send(ctx context.Context, b []byte) error {
	return c.ws.Write(ctx, websocket.MessageBinary, b)
}
func (c ch) Recv(ctx context.Context) ([]byte, error) {
	_, b, err := c.ws.Read(ctx)
	return b, err
}
func (c ch) Close() error { return c.ws.Close(websocket.StatusNormalClosure, "") }
