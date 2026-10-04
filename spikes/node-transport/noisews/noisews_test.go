package noisews

import (
	"bytes"
	"testing"

	"github.com/flynn/noise"
)

// The handshake's cryptography alone, all three messages in memory, with the
// node's pin check.
func BenchmarkHandshakeCrypto(b *testing.B) {
	hub, _ := NewHub()
	node, _ := NewNode()
	node.AddPin(hub.Pin())
	pin := node.pins[0]
	for b.Loop() {
		i, _ := noise.NewHandshakeState(noise.Config{CipherSuite: suite, Pattern: noise.HandshakeXX, Initiator: true, Prologue: prologue, StaticKeypair: node.key})
		r, _ := noise.NewHandshakeState(noise.Config{CipherSuite: suite, Pattern: noise.HandshakeXX, Prologue: prologue, StaticKeypair: hub.keys[0]})
		m1, _, _, _ := i.WriteMessage(nil, nil)
		if _, _, _, err := r.ReadMessage(nil, m1); err != nil {
			b.Fatal(err)
		}
		m2, _, _, _ := r.WriteMessage(nil, nil)
		if _, _, _, err := i.ReadMessage(nil, m2); err != nil {
			b.Fatal(err)
		}
		if !bytes.Equal(i.PeerStatic(), pin) {
			b.Fatal("pin")
		}
		m3, _, _, _ := i.WriteMessage(nil, nil)
		if _, _, _, err := r.ReadMessage(nil, m3); err != nil {
			b.Fatal(err)
		}
	}
}

// Handshake bytes on the wire, for the results table.
func TestHandshakeSize(t *testing.T) {
	hub, _ := NewHub()
	node, _ := NewNode()
	i, _ := noise.NewHandshakeState(noise.Config{CipherSuite: suite, Pattern: noise.HandshakeXX, Initiator: true, Prologue: prologue, StaticKeypair: node.key})
	r, _ := noise.NewHandshakeState(noise.Config{CipherSuite: suite, Pattern: noise.HandshakeXX, Prologue: prologue, StaticKeypair: hub.keys[0]})
	m1, _, _, _ := i.WriteMessage(nil, nil)
	r.ReadMessage(nil, m1)
	m2, _, _, _ := r.WriteMessage(nil, nil)
	i.ReadMessage(nil, m2)
	m3, _, _, _ := i.WriteMessage(nil, nil)
	t.Logf("Noise XX: %d + %d + %d = %d bytes", len(m1), len(m2), len(m3), len(m1)+len(m2)+len(m3))
}
