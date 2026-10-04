# Docker fixture: dogfood home (jupiter through its socket proxy)

The recorded, sanitized responses of the Docker engine on **jupiter** (Unraid, Docker 29.5.3, API 1.54), read through its socket-proxy container on `:2375`. They were captured read-only on 2026-10-04 by [spike 4.4](../../../spikes/docker-proxy/README.md). They seed the v0.1 Docker integration (the `containers` capability via a socket proxy), the proxy-vs-raw probe, and the correlator.

**pvedocker** was not captured. Its endpoint is a compose-network name (`dockerproxy`), which doesn't resolve outside that network. `pvedocker/capture.json` records that.

## Files (`jupiter/`)

| File | Docker API | Notes |
|---|---|---|
| `version.json` | `GET /version` | Server version, API 1.54, min API 1.40 |
| `containers.json` | `GET /v1.54/containers/json?all=1` | 74 containers. **At API 1.54 the summary includes `Health` {`Status`, `FailingStreak`}**, so health doesn't need an inspect. Older engines may lack it, so fall back to inspect |
| `inspect/<name>.json` | `GET /v1.54/containers/{id}/json` | One per container, named after the container |
| `events-past.json` | `GET /v1.54/events?since=-24h&until=now` | The newest 40 of the 256 events still in the engine's buffer. That buffer spanned only **332 s** (see `capture.json` → `eventStats`) |
| `events-live.json` | `GET /v1.54/events?since=now&until=now+55s` | 33 events in a 55 s window. Every one is a healthcheck `exec_create` / `exec_start` / `exec_die` |
| `probe.json` | various `GET`/`HEAD` | The proxy-vs-raw probe: one row per request, status code and content type only, plus what a raw Docker API would answer |
| `capture.json` | | Reachability, API version, ping headers, per-request statuses, event stats. `durations` include the spike's 250 ms pacing delay, so they aren't latencies |

There is no `info.json`, `images.json`, `networks.json` or `volumes.json`, because the proxy answers **403** to those (see the probe).

## How it was sanitized

Applied by `spikes/docker-proxy sanitize`, with the same domain and address map as the Traefik fixture, so the two still correlate.

| Original | Fixture |
|---|---|
| **Every env value** (`Config.Env`) | `KEY=REDACTED`. Keys are kept: 58 secret-looking keys across 29 containers (`*_KEY`, `*_TOKEN`, `*PASSWORD*`, `*SECRET*`, …), all redacted like the rest |
| Healthcheck outputs (`State.Health.Log[].Output`) | `REDACTED`. Exit codes and times are kept |
| Exec command lines in events (`exec_create: <cmd>`) | `exec_create: REDACTED` |
| Secret-looking labels (`homepage.widget.key`, `homepage.widget.password`) | Fake values in the same format. `homepage.widget.username` becomes `admin` |
| Domains in labels (`homepage.href`) | `*.lab.example` / `*.family.example`, as in the Homepage fixture |
| Emails (image `maintainer` labels) | `redacted@example.com` |
| Container, image, network, endpoint, sandbox and exec IDs, and every other 64-hex value | Re-hashed with a private salt. They stay consistent everywhere: `Id`, `Hostname` (its 12-char prefix), `HostsPath`, `LogPath`, event `Actor.ID`. The `@sha256:` digest in one image reference is re-hashed too, so it isn't a real digest |
| MAC addresses | `02:42:0a:00:xx:yy` |
| LAN addresses | `10.0.11.x` (jupiter `10.0.11.120`). Docker bridge `172.(16+n).c.d` → `10.(100+n).c.d` |
| A personal GitHub handle in one image name | `homelab-user` |
| Command-line flags that look like secrets (`--token=…`) and secret query parameters | `REDACTED` (none were present in this capture) |

Mount paths are unchanged: `/mnt/user/…` share names contain no personal names. Public project URLs in image labels (GitHub, linuxserver.io, project docs) are kept.

**Leak check:** `spikes/docker-proxy` has `TestFixturesLookSanitized`, which always runs: address ranges, MACs, every env value `REDACTED`, every healthcheck output `REDACTED`. It also has `TestFixturesHaveNoRealValues`, which scans for every real value seen during capture (2,921 of them, including the real env secrets and IDs) and runs with `LEAKCHECK_VALUES=<path>`. Both passed when the fixture was committed.

## What a consumer should get from it

Run `go run . analyze --fixtures ../../testdata/integrations/docker` in `spikes/docker-proxy` for the full table. The essentials:

- **74 containers:** 44 running, 21 exited, 9 created. 63 are Unraid template containers (`net.unraid.docker.managed=dockerman`) and 11 are Unraid Compose Manager containers (`composeman`, in projects `ai` ×5, `immich` ×4, `mealie`, `misc`).
- **Restart policy is `no` on 60 containers.** Unraid starts template containers from its own autostart list, so Docker can't say whether a stopped dockerman container should be running. That needs the Unraid API (spike 4.2).
- **Health:** 14 running containers are healthy and 30 have no healthcheck. The 5 non-healthy statuses (3 `starting`, 2 `unhealthy`) all belong to **exited** containers whose last health state is stale. Ignore `Health` unless `State == running`.
- `RestartCount` total 1 (`ai-whisperasr-1`). `OOMKilled` on 1 (`llama-swap`, exited).
- Containers from the Homepage fixture are all present: `Plex-Current` (host network), `radarr`, `sonarr`, `bazarr`, `tautulli`, `binhex-qbittorrentvpn`, `binhex-prowlarr`. So are the label-only services: `immich_server`, `mealie` and `proxmox-backup-server` carry `homepage.*` labels.
- **Two instances of one app:** `sonarr` + `sonarr-uhd` and `radarr` + `radarr-uhd` share an image and differ by name and port.
- **Open URLs without Traefik:** `net.unraid.docker.webui` (`http://[IP]:[PORT:8989]/`). Replace `[IP]` with the engine host and `[PORT:n]` with the host port bound to container port n (`HostConfig.PortBindings`). Host-network containers use n itself.
- **Stopped containers have no `Ports`** in the list. Use `HostConfig.PortBindings` from inspect to correlate routes to them.
- The socket proxy itself is the `dockersocket` container (`tecnativa/docker-socket-proxy`, published on all interfaces at `:2375`).
