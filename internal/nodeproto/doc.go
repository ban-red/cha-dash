// Package nodeproto is the wire protocol between the hub and chadash-node,
// shared by both (ADR-0014):
//
//   - The node dials the hub's /node endpoint (port 3696, beside the UI) and
//     upgrades to a WebSocket. Reverse proxies need WebSocket support and
//     nothing else.
//   - Inside the WebSocket, both sides run TLS 1.3 with self-signed Ed25519
//     certificates that are only envelopes: the node pins the hub's key
//     ("ed25519:…"), and the hub's registry pins the node's. There's no CA,
//     no name and no date check, so no clock is consulted. Go negotiates the
//     hybrid post-quantum X25519MLKEM768 key exchange by default. Session
//     tickets are off, so every connection is a full handshake and a replayed
//     one fails.
//   - The protocol version is negotiated with ALPN (chadash-node/1): the
//     node offers what it speaks, the hub picks the newest in common, and a
//     pair with nothing in common fails in the handshake.
//   - Frames are length-prefixed JSON (Frame), at most a MaxFrame each.
//     The node's hello comes first; the hub answers welcome or refused; then
//     a Session multiplexes RPC in both directions with streamed items,
//     cancellation and keepalive pings every 30 s, so proxies' idle timeouts
//     never cut a quiet channel and a dead peer is noticed in about 75 s.
//
// Keys live on disk as PEM-wrapped PKCS#8, 0600, written atomically
// (WriteKeyFile). Pins are public; private keys and pairing secrets never
// appear in logs, errors or frames other than the hello that carries them.
package nodeproto
