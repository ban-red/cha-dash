# Transmission RPC fixture (synthetic)

**Synthetic.** Nothing here was captured from a real Transmission. The answers were written by hand from Transmission's RPC specification ([`docs/rpc-spec.md`](https://github.com/transmission/transmission/blob/main/docs/rpc-spec.md) in the Transmission repository), in the shape Transmission 4.0 returns, trimmed to the fields the integration asks for plus a few for realism.

Every call is `POST /transmission/rpc` with a JSON body naming the method; the test's fake server routes on that method, answers 409 with an `X-Transmission-Session-Id` to a request without the current id, and wants basic auth.

| File | Method | Notes |
|---|---|---|
| `session_get.json` | `session-get` with `fields` | Transmission 4.0.6, RPC version 18, alternative speeds off |
| `session_stats.json` | `session-stats` | 2 350 000 B/s down (18.8 Mb/s), 480 000 B/s up (3.8 Mb/s) |
| `torrent_get.json` | `torrent-get` with `fields: [status, error]` | 12 torrents: 6 seeding (one with a tracker warning), 2 downloading (one with a tracker error), 1 queued to download, 1 queued to seed, 2 stopped; no local errors |
