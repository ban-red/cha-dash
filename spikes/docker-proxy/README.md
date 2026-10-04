# Spike 4.4 · Docker through the socket proxy

Throwaway code that answers: **what can the v0.1 hub read through a Homepage-style socket proxy, what does it need, and how do we tell a filtering proxy from a raw Docker API without ever writing?** It covers listing, inspect (with env redaction), events, a proxy-vs-raw probe, and the mapping from containers to compose projects, Unraid templates and apps. The sanitized recordings are in [`testdata/integrations/docker/`](../../testdata/integrations/docker/README.md).

Not product code. The findings go into the notes below. The real integration is written fresh.

## How it works

| Command | What it does |
|---|---|
| `capture` | For each endpoint in `DOCKER_ENDPOINTS`: a 4 s ping, then `/version` (picks the `/v1.xx` prefix), `/info`, `/containers/json?all=1`, an inspect per container, `/images/json`, `/networks` and `/volumes`. Then the past events still buffered (`since=-24h&until=now`, which returns at once), the probe, and a **live events window of 55 s** (`since=now&until=now+55s`, capped at 60 s in code) |
| `sanitize` | Fixtures from the raw capture: **every env value redacted**, healthcheck outputs and exec command lines redacted, secret-looking labels faked, IDs re-hashed, plus the domain and address map shared with the Traefik spike. It writes the private leak list |
| `analyze` | Runs on the **sanitized** fixtures, so its output is safe to print: summary, per-container table, events, probe verdict |

The `leakcheck` command and the shared plumbing live in [`spikes/traefik`](../traefik/README.md) (`labkit`).

**Safety, enforced in code** ([`guard.go`](guard.go), [`guard_test.go`](guard_test.go)):
- The labkit client refuses anything but GET/HEAD and requests with a body, and never follows redirects. **Writes are impossible.**
- On top of that, a **path allowlist** of read endpoints. `exec`, `attach`, `archive`, `export`, `top`, `build`, image pulls and everything else are refused before any network I/O.
- `/events` must carry both `since` and `until`. `/stats` must be `stream=false`.
- `/containers/{id}/logs` is only allowed as `stdout=0&stderr=0`. Docker answers that with **400 "you must choose at least one stream"** before it even looks up the container, so the probe can tell "the proxy let it through" (400) from "the proxy refused" (403) **without ever receiving a log line**.
- Requests are sequential, paced at ≥250 ms, with 25 s timeouts. Errors are reduced to their kind, so no address reaches a log.

## Run it

```bash
cd spikes/docker-proxy
LAB=/somewhere/outside/the/repo
go run . capture  --env ../../.env.local --raw $LAB/docker-raw
go run . sanitize --env ../../.env.local --raw $LAB/docker-raw --state $LAB/state.json \
                  --leak $LAB/leak-values.json --out ../../testdata/integrations/docker   # after the Traefik sanitize
(cd ../traefik && go run . leakcheck --values $LAB/leak-values.json ../../testdata/integrations/docker)
go run . analyze  --fixtures ../../testdata/integrations/docker
LEAKCHECK_VALUES=$LAB/leak-values.json go test ./...
```

## Findings

Captured on 2026-10-04.

### 1. Reachability
- **jupiter** (`tcp://<jupiter>:2375`): reachable. Docker **29.5.3**, API **1.54** (min 1.40), `Ostype: linux`, `Swarm: inactive`.
- **pvedocker** (the `dockerproxy` service name from Homepage's `docker.yaml`): **unreachable from this Mac** (DNS: name not found). It's a compose-network name. The hub, running in that compose project on pvedocker, would reach it. Recorded in `pvedocker/capture.json`.

### 2. The proxy-vs-raw probe (jupiter)

| Request | Group | Status | Raw Docker would answer |
|---|---|---|---|
| `HEAD /_ping` | needed | 200 (text/plain) | 200 |
| `GET /_ping` | needed | 200 (text/plain) | 200 |
| `GET /version` | needed | 200 (application/json) | 200 |
| `GET /v{api}/version` | needed | 200 (application/json) | 200 |
| `GET /v{api}/info` | needed | **403 (text/html)** | 200 |
| `GET /v{api}/events?since&until` | needed | 200 (application/jsonl) | 200 |
| `GET /v{api}/containers/json?all=1&limit=1` | needed | 200 (application/json) | 200 |
| `GET /v{api}/containers/{id}/json` | needed | 200 (application/json) | 200 |
| `GET /v{api}/containers/{id}/stats?stream=false&one-shot=true` | needed | 200 (application/json) | 200 |
| `GET /v{api}/images/json` | needed | **403 (text/html)** | 200 |
| `GET /v{api}/images/{image}/json` | needed | **403 (text/html)** | 200 |
| `GET /v{api}/networks` | needed | **403 (text/html)** | 200 |
| `GET /v{api}/networks/{network}` | needed | **403 (text/html)** | 200 |
| `GET /v{api}/volumes` | needed | **403 (text/html)** | 200 |
| `GET /v{api}/volumes/{volume}` | needed | **403 (text/html)** | 200 |
| `GET /v{api}/system/df?type=image` | extra | 403 (text/html) | 200 |
| `GET /v{api}/plugins` | extra | 403 (text/html) | 200 |
| `GET /v{api}/swarm` | blocked | 403 (text/html) | 503 (not a swarm manager) |
| `GET /v{api}/secrets` | blocked | 403 (text/html) | 503 |
| `GET /v{api}/configs` | blocked | 403 (text/html) | 503 |
| `GET /v{api}/services` | blocked | 403 (text/html) | 503 |
| `GET /v{api}/tasks` | blocked | 403 (text/html) | 503 |
| `GET /v{api}/nodes` | blocked | 403 (text/html) | 503 |
| `GET /v{api}/containers/{id}/logs?stdout=0&stderr=0&tail=0` | blocked | **400 (application/json), from Docker** | 400 |

**Verdict: a filtering proxy, but a coarse one.** It is **`tecnativa/docker-socket-proxy`** (HAProxy; its 403s are `text/html`), running as the `dockersocket` container and published on **all interfaces** at `:2375`. Its allow flags, read from its own container config (they're 0/1 switches, not secrets):

```
CONTAINERS=1  EVENTS=1  PING=1  VERSION=1  POST=0
everything else 0: INFO IMAGES NETWORKS VOLUMES SYSTEM PLUGINS SWARM SECRETS CONFIGS SERVICES TASKS NODES
                   AUTH BUILD COMMIT DISTRIBUTION EXEC GRPC SESSION ALLOW_START ALLOW_STOP ALLOW_RESTARTS
```

That matches the probe row for row. Writes are refused by `POST=0`. We never sent one: the client can't, and the read-only probe doesn't need to.

**The catch, and a credential-hygiene finding:** `CONTAINERS=1` opens **every GET under `/containers/`**. That's why the logs probe reached Docker. Anyone on the LAN who can reach `:2375` gets, with no credentials:
- **every container's environment** through inspect: 58 secret-looking keys across 29 containers here (VPN credentials, an OpenAI key, DB and OAuth secrets);
- **logs** (`/logs`), **any file in any container** (`GET /archive`) and **whole filesystems** (`GET /export`). None of these were requested: they are implied by the logs probe and Tecnativa's documented path matching.
- probably the attach websocket too (`GET /attach/ws` with an Upgrade), since `POST=0` only filters methods. Untested on purpose.

It isn't root on the host, but it's every secret on it. **Recommendation:** the hygiene check needs **three tiers**, not two:

| Probe result | Classification | Notice |
|---|---|---|
| Swarm/system endpoints answer like Docker (`503`/`200` JSON) | raw Docker API | **Critical:** "anyone who can reach this port has root on the host" |
| Those are refused, but `/containers/{id}/logs?stdout=0&stderr=0` returns Docker's **400** | coarse filtering proxy (Tecnativa-style `CONTAINERS=1`) | **Warning:** "this proxy shows every container's environment, logs and files to anyone who can reach it". Fix: restrict who can connect (firewall, or wollomatic's `-allowfrom`), or switch to a path-anchored allowlist |
| Those are refused **and** the logs probe gets **403** | path-anchored proxy (our compose preset) | none |

On this home jupiter falls in the middle tier. The Homepage fixture README currently expects **no** Docker-endpoint notice. That expectation should change (see the end of this file).

### 3. What the read model gets, and what it misses
- **`/containers/json?all=1`: 74 containers, 155 KB.** At API 1.54 the summary includes **`Health` {`Status`, `FailingStreak`}**, `State`, `Status` ("Up 4 weeks (healthy)", "Exited (137) 4 months ago"), `Labels`, `Ports`, `Mounts`, network names and `ImageID`. **Health needs no inspect.**
- **Inspect is still needed** for `RestartCount`, `HostConfig.RestartPolicy`, `State.OOMKilled` / `ExitCode` / `StartedAt` / `FinishedAt`, and for `HostConfig.PortBindings` of **stopped** containers (the list's `Ports` is empty for them; route correlation needs them, see 4.3). Inspect also carries the full env, so **redact on the hub, never forward it.**
- **Blocked here:** `/info` (engine facts), `/images*` (no RepoDigests, so no update checks), `/networks`, `/volumes`. The hub must treat a 403 (`text/html` from HAProxy, `text/plain` from wollomatic) as **"capability not granted by the proxy"**, not as an error. Probe once at connect and turn those features off.
- **Stats** (`stream=false&one-shot=true`) work, at about one request per running container. Only fetch them for containers on visible tiles. Host metrics come from Unraid or PVE anyway.

### 4. Containers → compose projects, Unraid templates, apps

| | |
|---|---|
| State | running 44 · exited 21 · created 9 |
| Managed by (`net.unraid.docker.managed`) | **dockerman 63** (Unraid templates) · **composeman 11** (Unraid's Compose Manager plugin) |
| Compose projects (`com.docker.compose.project`) | `ai` 5 · `immich` 4 · `mealie` 1 · `misc` 1. `working_dir` is `/boot/config/plugins/compose.manager/projects/<Name>`, i.e. compose files on the Unraid flash |
| Restart policy | **`no` 60** · `unless-stopped` 9 · `always` 5 |
| Health | healthy 14 · no healthcheck 55 · starting 3 · unhealthy 2. **All 5 non-healthy ones are exited containers with stale health** |
| Network mode | bridge 60 · compose networks 11 · host 2 (`Plex-Current`, `proxmox-backup-server`) · `br0` (macvlan, own LAN IP) 1 (`TimeMachine`) |
| Restarts · OOM | `RestartCount` total 1 · `OOMKilled` 1 (`llama-swap`) |

Selected rows (`go run . analyze` prints all 74):

| Container | Image | App | Managed by | State | Health | Restarts · policy | Open URL (from `net.unraid.docker.webui`) |
|---|---|---|---|---|---|---|---|
| `Plex-Current` | `lscr.io/linuxserver/plex:latest` | plex | dockerman | running | — | 0 · no | http://10.0.11.120:32400/web/index.html |
| `sonarr` | `binhex/arch-sonarr` | sonarr | dockerman | running | healthy | 0 · no | http://10.0.11.120:18989/ |
| `sonarr-uhd` | `binhex/arch-sonarr` | sonarr (2nd instance) | dockerman | running | healthy | 0 · no | http://10.0.11.120:18995/ |
| `radarr` | `binhex/arch-radarr` | radarr | dockerman | running | healthy | 0 · no | http://10.0.11.120:17878/ |
| `radarr-uhd` | `binhex/arch-radarr` | radarr (2nd instance) | dockerman | running | healthy | 0 · no | http://10.0.11.120:17879/ |
| `bazarr` | `lscr.io/linuxserver/bazarr:latest` | bazarr | dockerman | running | — | 0 · no | http://10.0.11.120:6767/system/status |
| `tautulli` | `lscr.io/linuxserver/tautulli:latest` | tautulli | dockerman | running | — | 0 · no | http://10.0.11.120:8181 |
| `binhex-qbittorrentvpn` | `binhex/arch-qbittorrentvpn:latest` | qbittorrent | dockerman | running | — | 0 · no | http://10.0.11.120:8112/ |
| `binhex-prowlarr` | `binhex/arch-prowlarr` | prowlarr | dockerman | running | healthy | 0 · no | http://10.0.11.120:9696/ |
| `immich_server` | `ghcr.io/immich-app/immich-server:v3` | Immich (`homepage.name`) | composeman · immich/immich-server | running | healthy | 0 · always | — |
| `immich_postgres` | `ghcr.io/immich-app/postgres:14-…` | immich | composeman · immich/database | running | healthy | 0 · always | — |
| `mealie` | `ghcr.io/mealie-recipes/mealie:nightly` | Mealie (`homepage.name`) | composeman · mealie/mealie | running | healthy | 0 · unless-stopped | — |
| `proxmox-backup-server` | `ayufan/proxmox-backup-server:latest` | PBS (`homepage.name`) | dockerman | running | — | 0 · unless-stopped | https://10.0.11.120:8007 |
| `nginxproxy` | `nginxproxy/nginx-proxy:alpine` | nginx-proxy (2nd-hop proxy, :7780) | dockerman | running | — | 0 · no | — |
| `dockersocket` | `ghcr.io/tecnativa/docker-socket-proxy:latest` | the socket proxy | dockerman | running | — | 0 · unless-stopped | — |
| `llama-swap` | `ghcr.io/mostlygeek/llama-swap:cuda` | ai | composeman · ai/llama-swap | exited (0) **OOM** | unhealthy (stale) | 0 · unless-stopped | — |
| `pingvin-share` | `stonith404/pingvin-share` | pingvin-share | dockerman | exited (137) | unhealthy (stale) | 0 · no | http://10.0.11.120:3301 |
| `binhex-krusader` | `binhex/arch-krusader` | krusader | dockerman | exited (255) | starting (stale) | 0 · no | http://10.0.11.120:6080/vnc.html?… |
| `ai-whisperasr-1` | `onerahmet/openai-whisper-asr-webservice:latest-gpu` | ai | composeman · ai/whisperasr | exited (0) | — | **1** · unless-stopped | — |

Mapping rules that held up:
- **App identity:** `homepage.name` when present (3 containers), else the compose project for multi-service stacks (`immich` ×4), else the image base name minus `arch-`/tag. Names alone fail: `Plex-Current`, `binhex-*`, and **two instances of one app** (`sonarr` and `sonarr-uhd` share an image). An app is image + instance, and the instance comes from the container name and port.
- **Unraid template vs compose:** `net.unraid.docker.managed=dockerman` is an Unraid template, so it's controlled through the Unraid API ("Unraid stays Unraid"). `composeman` is Unraid's Compose Manager: compose labels are present, but the files live on the flash and Unraid owns them, so **don't offer to adopt them as owned stacks**.
- **Open URL without Traefik:** `net.unraid.docker.webui` (63 containers) is a template, `http://[IP]:[PORT:8989]/`. Substitute the engine host for `[IP]` and the host port bound to container port 8989 for `[PORT:8989]`. Host-network containers use the port itself. One template (`jDownloader2`) names a container port with no binding, so expect misses.
- **Desired state:** dockerman containers use restart policy `no` (60 of them) and are started by Unraid's autostart list. **Docker alone can't tell "stopped on purpose" from "should be running"** for them, so the Unraid integration (4.2) has to supply autostart. For compose and others, `always`/`unless-stopped` means "should run".
- **Health for the gate and tiles:** use `Health.Status` only when `State == running`. Treat `FailingStreak > 0` on a running container as attention. Use `OOMKilled` and `ExitCode` (in `Status` too) for exited ones. Restart loops show as `RestartCount` climbing between polls, or as `restart`/`die` events.

### 5. Events
- **Every event in both windows was a healthcheck exec:** `exec_create` / `exec_start` / `exec_die`, at **≈40–46 per minute** (14 running containers have healthchecks, and each check makes 3 events). No lifecycle events happened, and no `health_status` events (Docker emits those only when the status changes).
- `exec_create`/`exec_start` actions embed the **healthcheck command line** (`"exec_start: /bin/sh -c curl …"`). Every event's `Actor.Attributes` carries **all the container's labels**, including secrets people put in labels (`homepage.widget.key`, `homepage.widget.password` are set on 3 containers here). Never log raw events. Scrub them like inspect output.
- **The engine buffers only 256 events.** On this host that was **332 seconds** of history, all exec noise. `since=` replay after a disconnect covers about 5 minutes at most.

### 6. Polling vs events: recommended strategy
1. **Connect:** ping, then `/version` (pin the API prefix), probe capabilities once (`/info`, `/images/json` → on/off), then **list** (`/containers/json?all=1`) as the snapshot. Inspect every container once with low concurrency (2–4); 74 inspects is a few seconds.
2. **Subscribe** to `/events` with a server-side filter, so healthcheck execs never cross the wire: `filters={"type":["container"],"event":["create","start","restart","die","stop","kill","oom","pause","unpause","destroy","rename","update","health_status"]}`. Moby fuzzy-matches `health_status` against `health_status: healthy`. On each event, re-inspect just that container. `die` carries `exitCode` in its attributes.
3. **Reconnect = re-list.** Don't trust `since` replay (256-event buffer). Expect the proxy (HAProxy) to cut long idle streams, and reconnect with backoff.
4. **Reconcile:** re-list every 60 s anyway (155 KB, one request) to catch missed events and `FailingStreak` changes that don't emit events. Re-inspect only containers whose `State`/`Status`/`Health` changed.
5. **Stats:** one-shot, only for containers on visible tiles, every 15–30 s, sequentially.

### 7. Other hygiene signals seen (for the Inbox)
- Two containers (`coder`, `Onedev`) mount `/var/run/docker.sock` read-write, which is full Docker access. `nginxproxy` and `dockersocket` mount it `:ro`, but a read-only bind of a socket does **not** make the API read-only.
- 8 secret-looking env values are short lowercase words (default-style passwords). Counted only; the values never left the capture.
- Secrets in labels (above) are visible to anyone who can list containers, and ride along on every event.

## Least-privilege proxy allowlist for the hub (v0.1)

What the hub actually calls, compared with the socket-proxy preset in [`deploy/compose.yaml`](../../deploy/compose.yaml) (wollomatic, path-anchored regexps):

| Endpoint | Hub v0.1 | Preset today | Recommendation |
|---|---|---|---|
| `HEAD`/`GET /_ping`, `GET /version` | **required** (negotiation, liveness) | allowed | keep |
| `GET /containers/json` | **required** | allowed | keep |
| `GET /containers/{id}/json` | **required** (restart count, policy, OOM, stopped containers' port bindings) | allowed | keep. The env is in there: redact on the hub |
| `GET /events` | **required** | allowed | keep (filters are a query string, path matching is unaffected) |
| `GET /containers/{id}/stats` | optional (per-container CPU/memory tiles) | allowed | keep |
| `GET /info` | optional (engine name, counts, version) | allowed | keep, but the hub must work without it (jupiter blocks it) |
| `GET /images/json`, `/images/{name}/json` | optional (tags, digests for update checks) | allowed | keep as optional. Update checks need it |
| `GET /networks(/…)`, `/volumes(/…)` | **not used** (network names come in the container summary, volume names in `Mounts`) | allowed | **drop from the preset** (or comment them out as opt-in) |
| `/containers/{id}/logs`, `archive`, `export`, `top`, `attach/ws`, `exec` | never (reads) / never (writes) | refused (not on the list) | keep refused. This is exactly what Tecnativa's `CONTAINERS=1` leaks |

The preset's regex `/(v1\.[0-9]+/)?…` matches API 1.54 paths. Our compose preset already lands in the safe tier of the classifier above: a logs probe would get 403.

**For users who already run Tecnativa** (the Homepage-style setup, the importer's 1:1 path), the closest least-privilege equivalent is `CONTAINERS=1 EVENTS=1 PING=1 VERSION=1 POST=0`, plus `INFO=1 IMAGES=1` if wanted. Document that it still exposes logs, files and env, and that `:2375` should not be published to the whole LAN.

## What the main session should update
1. **`deploy/compose.yaml` socket-proxy preset:** drop (or comment out as opt-in) the `networks` and `volumes` allow rules. The hub doesn't use them. Everything else stays.
2. **`testdata/importers/homepage/dogfood/README.md` and `docs/plan/05-dogfood-home.md`:** jupiter's endpoint is a *coarse* filtering proxy (Tecnativa, `CONTAINERS=1`, published on all interfaces). Under the three-tier check above it should raise the **warning** tier ("exposes every container's env, logs and files to the LAN"), not "no notice". pvedocker's proxy is still unprobed (unreachable from the spike host). Re-probe it from the hub on pvedocker.
3. **`docs/plan/03-architecture.md`, credential hygiene:** turn "raw API vs filtering proxy" into the three tiers. Add "secrets in container labels", "containers with the Docker socket mounted", and, from 4.3, "Traefik credentials over http" and "dashboard without auth on a LAN name".
4. **Env redaction design:** keep a small server-side allowlist of non-secret env keys that are read for correlation *before* redaction (`VIRTUAL_HOST`, `VIRTUAL_PATH`, `VIRTUAL_PORT`, `VIRTUAL_HOST_MULTIPORTS`, maybe `TZ`). They're needed to follow nginx-proxy hops (4.3). Never send any env value to the browser.
5. **Unraid integration (4.2):** must supply **autostart** to get desired state for dockerman containers. Docker restart policy is `no` for 60 of 74.
