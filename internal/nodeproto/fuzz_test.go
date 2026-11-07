package nodeproto

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// FuzzFrame: what anyone on the network can send the hub before it knows
// them. A length-prefixed frame under the handshake limit, read as the hub
// reads a hello: the Frame, the Hello in it, and the pairing or join token
// it carries. Nothing panics; a body is exactly the size its header says
// and never over the limit; a token that parses is the one formatted from
// its parts.
//
//	go test -run '^$' -fuzz '^FuzzFrame$' -fuzztime 60s ./internal/nodeproto/
func FuzzFrame(f *testing.F) {
	pin := strings.Repeat("A", 43) // the shape only; ParsePin decides
	for _, body := range []string{
		`{"op":"hello","data":{"versions":[1],"name":"local","pairing":"0123456789abcdef0123"}}`,
		`{"op":"hello","data":{"name":"nas","token":"cdj_0123456789abcdef.` + pin + `"}}`,
		`{"op":"hello","data":{"name":"x","verbs":[{"name":"docker.ps","policy":"docker.read","allowed":true}],"hub_pins":["` + pin + `"]}}`,
		`{"op":"req","id":1,"method":"stack.list","data":{}}`,
		`{"op":"hello","data":null}`,
		`{"op":"hello","data":"cdp_x.y"}`,
		`[]`,
		``,
	} {
		f.Add(frameBytes(uint32(len(body)), body))
	}
	f.Add(frameBytes(HandshakeMaxFrame+1, "{}"))
	f.Add(frameBytes(10, `{"op"`)) // shorter than its header says
	f.Add([]byte{0, 0})

	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) < 4 {
			return
		}
		size := binary.BigEndian.Uint32(b[:4])
		rest := b[4:]
		body, err := readBody(bytes.NewReader(rest), size, HandshakeMaxFrame)
		switch {
		case int64(size) > HandshakeMaxFrame:
			if !errors.Is(err, ErrFrameTooLarge) {
				t.Fatalf("a %d-byte frame over the limit: %v", size, err)
			}
			return
		case int64(len(rest)) < int64(size):
			if err == nil {
				t.Fatalf("a short frame read without an error")
			}
			return
		case err != nil:
			t.Fatalf("a whole frame: %v", err)
		case !bytes.Equal(body, rest[:size]):
			t.Fatalf("the body isn't the %d bytes after the header", size)
		}

		var fr Frame
		if json.Unmarshal(body, &fr) != nil || fr.Op != OpHello {
			return
		}
		var hello Hello
		if json.Unmarshal(fr.Data, &hello) != nil {
			return
		}
		for _, tok := range []string{hello.Pairing, hello.Token, pairingPrefix + hello.Pairing, joinPrefix + hello.Token} {
			if secret, hubPin, err := ParsePairing(tok); err == nil && FormatPairing(secret, hubPin) != strings.TrimSpace(tok) {
				t.Fatalf("pairing token %q parsed to %q and %q", tok, secret, hubPin)
			}
			if secret, hubPin, err := ParseJoin(tok); err == nil && FormatJoin(secret, hubPin) != strings.TrimSpace(tok) {
				t.Fatalf("join token %q parsed to %q and %q", tok, secret, hubPin)
			}
		}
	})
}

func frameBytes(size uint32, body string) []byte {
	b := binary.BigEndian.AppendUint32(nil, size)
	return append(b, body...)
}
