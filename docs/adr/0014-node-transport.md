# ADR-0014 · Nodes and the hub speak mutual TLS inside the WebSocket, pinned by key

**Status:** accepted · **Date:** 2026-10-03 · **Roadmap:** [decision 14](../plan/04-roadmap.md#decisions) · **Evidence:** [spike 4.7 findings](../../spikes/node-transport/README.md#findings)

## Context

The node is root on its host, and the hub holds every credential in the house ([ADR-0006](0006-owned-stacks-and-thin-node.md)). The channel between them must survive a rogue hub, a rogue node and a man in the middle ([threat model](../plan/03-architecture.md#threat-model-summary-full-version-in-securitymd)). It also carries stack secrets, which the node resolves from the hub's vault at deploy time. The node dials **out** to `/node` on port 3696 ([ADR-0009](0009-default-port-3696.md)), usually through the reverse proxy that serves the UI, and that proxy terminates TLS. The v0.2 local node uses plain `ws://` on the compose network. The roadmap leaned towards mTLS with a pinned self-signed CA, unless Noise proved better. Spike 4.7 built three designs on one shared core and ran the same 13 scenarios against each: mTLS with a CA, mTLS with pinned keys and no CA, and Noise XX.

## Decision

**The node and the hub run mutual TLS 1.3 inside the WebSocket. Each side is identified by its Ed25519 public key, pinned at enrollment. There's no CA.**
- **Inside the WebSocket**, because outer mTLS fails behind a TLS-terminating proxy: the spike's hub got no client certificate (401). The inner session is end to end, whatever the proxy, and over plain `ws://` too.
- **Pinned keys, no CA.** Both sides present self-signed certificates that serve only as envelopes. The node pins the hub's key (`ed25519:…`, the form `policy.yaml` already shows). The hub's registry pins the node's. Nothing is issued and nothing expires, and no clock is consulted.
- **Standard library only.** `crypto/tls` runs over `websocket.NetConn` from `coder/websocket`. Go negotiates **X25519MLKEM768**, a hybrid post-quantum key exchange, by default.

**Evidence:** all three designs passed every scenario. That includes the attacks: an impostor hub, an unenrolled node, a stolen certificate or public key, tampering, replay, token reuse and revocation. Pinned TLS had the least code (239 lines, against 403 with a CA and 246 for Noise) and no new dependency. Its binary delta was under 130 KiB. A reconnect took 0.52 ms (0.59 ms with a CA, 0.57 ms with Noise). The CA design failed with a node clock at 1970, and with an expired hub certificate.

## Consequences

- **Enrollment.**
  - "Add node" mints `cdj_<secret>.<hub pin>`: single use, 15 minutes. The hub stores only its hash. **The token carries the hub's pin, so the node authenticates the hub before it sends the secret.** Enrollment isn't trust-on-first-use, and a man in the middle never sees the token (tested).
  - The node's first frame presents the token, and the node appears in the Inbox as pending. The approval card shows the node's hostname, its address and its key fingerprint, which the node also prints in its log.
  - A node that restarts while pending stays pending. A spent token left in a compose file is harmless.
- **On the node,** in `/etc/chadash-node/`:
  - `policy.yaml` is owner-written and may be mounted read-only. It can fix `hub.pin` and forbid pushed pins.
  - `identity/node.key` is the private key: PKCS#8, `0600`, written atomically. `identity/node.next.key` exists only during a rotation.
  - `identity/state.json` holds the node ID, the hub URL and the pinned hub keys.
- **On the hub:**
  - a `nodes` table: ID, name, public key (unique), next key, state (pending · approved · revoked), who approved or revoked it and when, last seen, address and version;
  - `node_join_tokens`: hash, expiry, creator, use;
  - the hub's private keys, sealed in the vault, with a `retired_at` during rollover;
  - audit rows for enroll, approve, rotate and revoke.
- **Rotation.**
  - A node rotates over its live session. The hub holds both keys until it first sees the new one, so a crash in between is safe.
  - The hub rotates by pushing its next pin to connected nodes. Nodes offline at the time get it on their next connection; the node lists its pins on the upgrade, so the hub answers with a key the node trusts. After a window the old key is retired; the UI first lists the nodes that haven't picked up the new key.
  - Rotation is hygiene, not compromise recovery: whoever steals a hub key can push a pin too. After a compromise, re-enroll every node.
- **Revocation** is a registry state, checked on every handshake. Revoking cuts a live session at once. There's no CRL and no OCSP, because the registry is the list.
- **Proxies** need WebSocket support and nothing else: no TLS passthrough, no second port, no forwarded-certificate headers. They see only ciphertext (tested). The channel should ping every 30 s, as the browser socket does, to stay inside proxies' idle timeouts.
- **Costs.**
  - Behind a proxy, traffic is encrypted twice.
  - TLS inside a WebSocket is unusual, so reviewers need this ADR.
  - The node's pin check uses `InsecureSkipVerify` with `VerifyConnection`. That needs a comment, a lint exception and the impostor tests as its guard.
  - Each hub key rotation is a rollover with a window. Keys never expire, so rotation and revocation carry the load.
- **Follow-up.**
  - Write the channel fresh in `internal/` and the node: identities on disk, the two tables, per-node pin acknowledgements, and enrollment rate-limited per client IP.
  - Pair the v0.2 local node automatically.
  - Decide whether direct mode (the hub dials the node) is worth building.

## Alternatives considered

- **Outer mTLS with a hub-run CA.** It fails behind a TLS-terminating proxy. It would need passthrough or a second port in every proxy, it can't work through Cloudflare Tunnel's HTTP mode, and it doesn't fit plain `ws://` on the compose network.
- **mTLS with a CA, inside the WebSocket** (the spike's design 1). It works. But it needs 1.7 times the code (403 lines against 239), fails on wrong clocks and expired certificates, and needs a re-issue round trip for every node after a CA rollover. The CA adds little where the hub is the only verifier and checks a registry anyway.
- **Noise XX inside the WebSocket** (design 2). It works, with a 192-byte handshake and no x509. But it adds `flynn/noise` (last released February 2024), has no post-quantum option, needs its own chunking and careful nonce ordering, and fewer reviewers know it. It's a sound fallback if TLS-in-WebSocket ever proves awkward.
- **The proxy forwards the client certificate in a header.** That means per-proxy setup, and the proxy joins the trust base.
- **Channel binding to the outer TLS** (RFC 9266). Each leg of a proxied connection has its own TLS session, so there's nothing to bind to.
- **A WireGuard or tsnet-style tunnel.** It needs UDP reachability or a coordination server, brings a large dependency and a whole network stack, and doesn't pass through HTTP proxies. A tailnet can still carry `CHADASH_HUB`, with this channel inside it.
