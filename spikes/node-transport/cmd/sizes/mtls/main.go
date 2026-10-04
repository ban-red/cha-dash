// Command mtls is the node end of design 1, for measuring binary size.
package main

import (
	"context"
	"log"
	"os"

	"dash.cha.sh/spikes/node-transport/core"
	"dash.cha.sh/spikes/node-transport/mtlsws"
)

func main() {
	nc, err := mtlsws.NewNode()
	if err != nil {
		log.Fatal(err)
	}
	n, err := core.NewNode(nc, os.Getenv("CHADASH_HUB"), os.Getenv("CHADASH_JOIN_TOKEN"))
	if err != nil {
		log.Fatal(err)
	}
	log.Fatal(n.Run(context.Background()))
}
