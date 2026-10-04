# NZBGet JSON-RPC fixture (synthetic)

**Synthetic.** Nothing here was captured from a real NZBGet. The answers were written by hand from NZBGet's API reference (the [`status`](https://nzbget.com/documentation/api/methods/status), `version` and `listgroups` methods, and `daemon/remote/XmlRpc.cpp` in the [nzbgetcom/nzbget](https://github.com/nzbgetcom/nzbget) repository), in the shape NZBGet 24 returns. Download names are made up.

Every call is `POST /jsonrpc` with `{"version": "1.1", "id", "method", "params"}`; the test's fake server routes on the method and wants basic auth.

| File | Method | Notes |
|---|---|---|
| `version.json` | `version` | `24.8` |
| `status.json` | `status` | 2 350 000 B/s (18.8 Mb/s), 6839 MiB left (6.7 GiB), not paused, 1 287 577 MiB free (1257.4 GiB), one post-processing job |
| `listgroups.json` | `listgroups` with `[0]` | 3 groups: downloading, queued, unpacking |
