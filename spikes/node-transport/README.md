# Spike 4.7 · node transport and identity

Throwaway code that answers one question for [ADR-0014](../../docs/adr/0014-node-transport.md): **how should `chadash-node` and the hub authenticate each other and encrypt the channel between them**, given that the node dials out over a WebSocket, often through a reverse proxy that terminates TLS? The roadmap's candidates were mTLS with a pinned self-signed CA, or a Noise handshake.

Not product code. The findings go into the notes below and into the ADR; the real channel is written fresh in the hub and node.

## What was built

One shared core and three interchangeable designs. Every design runs **inside the WebSocket**, so the proxy in front of the hub carries it as opaque binary messages.

| Package | What it is |
|---|---|
| [`core`](core) | Everything that doesn't depend on the handshake: join tokens (hashed, single use, carrying the hub's pin), the node registry, JSON RPC frames with streams, the hub's `/node` handler and the node's dial loop. Each design plugs in a `HubCrypto` and a `NodeCrypto` |
| [`mtlsws`](mtlsws) | **Design 1, mTLS with a CA.** The hub runs an Ed25519 CA. The node pins the CA's SPKI hash. Before approval the node presents a self-signed certificate and a CSR; at approval the hub signs a client certificate. TLS 1.3 runs inside the WebSocket |
| [`pinnedtls`](pinnedtls) | **Design 1b, mTLS without a CA.** Added during the spike, once the CA turned out to be most of design 1's cost. Same TLS 1.3 inside the WebSocket, but both ends present self-signed Ed25519 certificates and are known by key alone. The node pins the hub's key, and the registry pins the node's |
| [`noisews`](noisews) | **Design 2, Noise.** `Noise_XX_25519_ChaChaPoly_BLAKE2s` ([flynn/noise](https://github.com/flynn/noise)) over binary WebSocket messages. Static keys are pinned at enrollment. Frames are chunked to fit Noise's 64 KiB message limit |
| [`internal/testnet`](internal/testnet) | In-process stand-ins for what sits between node and hub: a TLS-terminating reverse proxy, a TCP passthrough, and a WebSocket relay that records, tampers with and replays messages |
| [`cmd/sizes`](cmd/sizes) | Four node `main`s that differ only by design (plus a plain baseline), for binary size and dependency counts |

**How a node joins, in every design:**
1. "Add node" mints `cdj_<secret>.<hub pin>`: 130 random bits, valid 15 minutes, single use. The hub stores only its SHA-256.
2. The node generates its key, pins the hub from the token, and dials `/node`. **It checks the hub's identity before it sends anything**, so a man in the middle never sees the token. Enrollment isn't trust-on-first-use.
3. The node's first frame is `hello` with the token. The hub spends the token and holds the node as **pending**. A pending node that restarts comes back as the same pending node; its key is enough.
4. An admin approves. The hub answers `approved` (design 1 includes the signed certificate), and the node reconnects into a full session: RPC in both directions, with streams.

The node also sends the hub pins it holds in a `Chadash-Hub-Pins` header on the upgrade, so during a hub key rollover the hub answers each node with an identity it trusts.

## Run it

Everything runs in-process on loopback. No Docker, no network.

```bash
cd spikes/node-transport
go test -race ./...                                   # the scenario suite, all designs
go test -run '^$' -bench . -benchmem -count=6 ./...   # benchmarks
out=$(mktemp -d) && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o "$out/" ./cmd/sizes/... && ls -l "$out"   # sizes
```

## Scenarios

One suite ([`transport_test.go`](transport_test.go)) runs against all three designs through the same core, so a different outcome can only come from the design. **All three pass every scenario.** That's 76 test cases, run 10× under `-race` and 20× without, with no flakes.

| Scenario | What it proves |
|---|---|
| Enroll and call, direct and **behind a TLS-terminating proxy** | Token → pending → approve → session; a ping, a 500-item stream and a 300 KiB echo, on plain `ws://` (the compose network) and on `wss://` through a reverse proxy |
| Reconnect | An approved node reconnects with its pinned identity, including after a restart that reloads it, with no token |
| Pending survives a restart | A node that restarts before approval comes back as the same pending node |
| Join token rules | A second use, an expired token and a forged secret are all refused with the same error |
| **Unenrolled node** | A node with a valid handshake but a key the hub doesn't know gets `unknown node` and no session |
| **Impostor hub (MITM)** | A hub with its own keys at the URL: the node aborts in the handshake, the impostor never sees a frame, and the token still works at the real hub |
| **Proxy sees only ciphertext** | A relay at the proxy's position sees neither the token, the node ID, frame names nor RPC content |
| **Tampering** | One flipped bit in either direction ends the session instead of delivering altered data |
| **Replay** | Replaying a recorded handshake fails at the hub and opens no session |
| **Stolen public identity** | A node's certificate or public key, without its private key, gets nothing |
| Revocation | Revoking cuts a live session at once and refuses later attempts, including a node still pending |
| Node key rotation | Over the live session; the hub accepts both keys until it first sees the new one, then only the new one |
| Hub key rotation | Connected nodes get the new pin pushed; offline nodes get it on their next connection; after the old key is retired, only nodes that missed the whole window must re-enroll |

Design-specific checks: outer mTLS through three paths ([`outer_mtls_test.go`](outer_mtls_test.go)); forged chains against design 1's pin check; leaf rotation and the clock failures in design 1 ([`mtls_only_test.go`](mtls_only_test.go)); design 1b with clocks at 1970 and 2200; handshake sizes and the negotiated key exchange.

## Results

Go 1.27.1. Benchmarks on darwin/arm64 (Apple M4 Pro), median of 6. Sizes are stripped static linux builds. "Code" counts non-blank, non-comment lines, without test hooks.

| | 1 · mTLS + CA | 1b · mTLS, pinned keys | 2 · Noise XX |
|---|---|---|---|
| **Scenario suite** | ✅ all pass | ✅ all pass | ✅ all pass |
| **Design code** (on top of a 761-line shared core) | **403** lines | **239** | **246** |
| ↳ of which framing | 35 | 34 | 54 (incl. chunking) |
| **New modules** | none (stdlib) | none (stdlib) | `flynn/noise` v1.1.0 (811 lines, last release Feb 2024) + `x/crypto`, which hub and node already have |
| **Binary delta**, linux/amd64 · arm64 (base 6.9 · 6.4 MB) | +136 · +128 KiB | +120 · +128 KiB | +172 · +64 KiB |
| **Full reconnect**, loopback: upgrade + handshake + welcome (plain WebSocket floor: 135 µs) | 589 µs | 521 µs | 568 µs |
| **Handshake crypto only** | 398 µs | 334 µs | 382 µs |
| **Handshake bytes** | 4,254 | 3,663 | 192 |
| **Request/response** on a live session | 33 µs | 35 µs | 32 µs |
| **1 MiB echo** through RPC | 369 MB/s | 353 MB/s | 278 MB/s |
| **Key exchange** | X25519MLKEM768 (hybrid post-quantum), by default | X25519MLKEM768, by default | X25519 |
| **Behind a TLS-terminating proxy** | ✅ (inside the WebSocket) | ✅ | ✅ |
| **Needs a correct clock** | **Yes**: a node at 1970 or an expired hub leaf can't connect | No | No |
| **Hub key rotation** | Leaf: invisible. CA: pin push + overlap, then **one extra reconnect per node** to re-issue its certificate | Pin push + overlap | Pin push + overlap |
| **Node key rotation** | CSR over the session, certificate back | Public key over the session | Public key over the session |
| **Revocation** | Registry lookup on every handshake; live session cut | Same | Same |
| **Custom verification code** | Pin check, chain building, name and date checks (`InsecureSkipVerify` + `VerifyConnection`) | One pin comparison (`InsecureSkipVerify` + `VerifyConnection`) | One pin comparison after message 2 |

**Outer mTLS**, the textbook setup, for comparison ([`outer_mtls_test.go`](outer_mtls_test.go)):

| Path from node to hub | Outer mTLS | Any design above |
|---|---|---|
| Direct | ✅ 200 | ✅ |
| TCP passthrough (Traefik TCP router, Caddy layer4, nginx `stream`) | ✅ 200 | ✅ |
| **TLS-terminating proxy** (the default for Traefik, Caddy, nginx, Cloudflare Tunnel) | ❌ **401: the hub never sees a client certificate** | ✅ |
| Plain `ws://` on the compose network (the v0.2 local node) | ❌ needs the hub to terminate TLS | ✅ |

## Findings

**Verdict: design 1b, mutual TLS 1.3 inside the WebSocket with both ends pinned by Ed25519 key, and no CA.** It passes everything the others pass, with the least code, no new dependency, the post-quantum key exchange Go now does by default, and no dependence on clocks or expiry.

1. **Whatever we pick has to run inside the WebSocket.** Outer mTLS only works when the node's TLS reaches the hub untouched. Behind a terminating proxy the proxy opens its own connection and has no client certificate to give. Passthrough or a second port would mean per-proxy setup (Cloudflare Tunnel's HTTP mode can't pass a client certificate through at all), and the v0.2 local node talks plain `ws://` on the compose network. Channel binding to the outer TLS (RFC 9266 `tls-exporter`) can't help either, because each leg of a proxied connection has its own TLS session. Inside the WebSocket, all three designs keep `/node` on port 3696 behind the same proxy as the UI ([ADR-0009](../../docs/adr/0009-default-port-3696.md)), and the proxy sees only ciphertext.
2. **Security is a draw.** The same 13 scenarios pass for all three: impostor hub, unenrolled node, stolen public identity, tampering, replay, token reuse, revocation and both rotations. What decides is cost and failure modes.
3. **The registry is the authority, so a CA adds little.** There's one verifier (the hub), and it looks every node up by public key on every handshake. That lookup is the revocation list, and it's what admits or refuses a node. The CA's signature on node certificates is belt and braces. Expiry protects nothing, because the hub re-issues to any approved key. The CA's one real gift is invisible hub leaf rotation, and the CA key sits online next to the leaf, in the same `/data`. CA rollover still needs the same pin push as raw keys, plus a re-issue reconnect for every node.
4. **Certificates bring clocks.** With design 1, a node whose clock reads 1970 can't connect: think of a board without a real-time clock, before NTP. An expired hub leaf takes every node offline until it's replaced. Both are tested. Homelabs have exactly these machines. Designs 1b and 2 never consult a clock (1b is tested at 1970 and 2200).
5. **TLS gets post-quantum key exchange for free; flynn/noise doesn't.** Go 1.27's TLS 1.3 negotiates X25519MLKEM768 with no configuration. The channel carries stack secrets, which the node resolves from the hub's vault at deploy time ([03 · Architecture](../../docs/plan/03-architecture.md#stacks-cha-dash-owns-them-)), so harvest-now-decrypt-later is a real concern. Noise would need a hybrid pattern we'd have to build or wait for.
6. **Performance doesn't decide it.** All three reconnect in about 0.5–0.6 ms on loopback, against a 0.14 ms floor for the bare WebSocket. A request/response round trip is ~33 µs in each. TLS moves bulk data faster (350–370 MB/s against 280) with fewer allocations, because Noise needs chunking. Noise's 192-byte handshake is elegant, but a connection that lives for days doesn't care about 3.6 KB.
7. **Noise is the simplest protocol, but not the simplest code here.** Its whole handshake fits on one screen, and there's no x509 parser in front of untrusted input. But the design has its own sharp edges, each handled in the spike:
   - the 64 KiB message limit (chunking);
   - nonce ordering under concurrency (encrypt and write under one lock, or the session desyncs);
   - getting the cipher states the right way round;
   - a small outside library, last released in February 2024.

   Its reviewers also need to know Noise, and far more Go reviewers know `crypto/tls`.
8. **Both TLS designs carry an `InsecureSkipVerify`.** The node checks the hub with `VerifyConnection` instead of the system roots. In 1b that's a single pin comparison, easy to review, but linters will flag it, so it needs a comment and a lint exception. The impostor and stolen-identity tests are its regression guard.
9. **Rotation is the same shape everywhere.** A node rotates over the live session, and the hub holds both keys until it sees the new one, so a crash in between leaves the node on its old key, still connected. The hub rotates by pushing its next pin over a channel the current key authenticates. Rotation is hygiene, not compromise recovery: whoever steals the hub's key can push their own pin too. After a compromise, re-enroll, and let local policy refuse pushed pins.
10. **It fits the hub as is.** `coder/websocket` (already a dependency) gives a `net.Conn` with `websocket.NetConn`. `crypto/tls` runs on it, and the frames reuse the browser protocol's JSON `op` style. The registry is two tables on the existing SQLite store, with tokens hashed like sessions. `/node` sits outside the session middleware and the Origin check. A node has no cookie and sends no Origin.

**Carry into the real node and hub:**
- keys on disk: PKCS#8, `0600`, written atomically, with a `next` key during rotation;
- per-node tracking of which hub pins each node has acknowledged, so the UI can say who'd be stranded before the old key is retired;
- `hub.pin` and a `pin_rotation` switch in `policy.yaml`;
- enrollment rate-limited per client IP, like login;
- the node's key fingerprint shown on the approval card and printed in the node's log, so the admin can compare the two;
- automatic pairing for the v0.2 local node.

## Known gaps (deliberately out of scope)

- Identities live in memory. Nothing is written to disk, and nothing is stored in SQLite.
- Direct mode (the hub dials the node) isn't built. The handshake roles would stay the same: the hub keeps the server side, and the node still checks the hub's pin.
- No backpressure in the RPC layer beyond a 16-frame buffer per call, and no message size policy beyond a 16 MiB frame cap.
- No ack tracking for hub key rollover: the hub pushes and forgets.
- No keepalive pings on the node channel. The real one should ping every 30 s, like the browser socket.
- Not measured on a real proxy (Traefik, Caddy) or across a real network. The in-process reverse proxy is Go's `httputil.ReverseProxy`, which passes WebSocket upgrades the same way.
