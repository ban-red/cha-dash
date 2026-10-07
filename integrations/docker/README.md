# Docker (`docker`)

Reads one Docker engine: the engine, its containers and their compose stacks, live while someone watches. It reads through a socket proxy, the socket itself, or chadash-node (`node://<name>`, v0.2). Through a proxy or the socket it never writes; through a node it also offers [actions](#actions-through-the-node). Containers keep an allowlist of their labels, so `homepage.*` labels arrive ready for label discovery.

**Tier:** silver (repairs, version detection, a capability probe, fixture tests). **Capability:** `containers`. **Secrets:** none.

Built from [spike 4.4](../../spikes/docker-proxy/README.md) and tested against its capture of jupiter's engine behind a Tecnativa proxy ([`testdata/integrations/docker/jupiter`](../../testdata/integrations/docker/README.md)). The anchored-proxy, raw-API and socket cases in the tests are synthetic variations of that capture.

## Config

```json
{"endpoint": "tcp://10.0.11.120:2375"}
{"endpoint": "tcp://dockerproxy:2375"}
{"endpoint": "unix:///var/run/docker.sock"}
{"endpoint": "node://local"}
{"endpoint": "tcp://docker.lab.example:2376", "tls": {"mode": "tofu"}}
```

| Key | Meaning |
|---|---|
| `endpoint` | `tcp://host[:port]` (port 2375, or 2376 with `tls`), `unix:///absolute/path`, or `node://<node name or ID>` for chadash-node (`node://local` on the hub's host). The first two are the shapes the Homepage importer writes. |
| `tls` | Only for an endpoint that speaks TLS. Its presence switches the client to `https://`, so the schema gives `mode` no default. A Unix socket can't have one. Client certificates (Docker's `--tlsverify`) aren't supported yet. |

## What it reads

Every request is a `GET`. Paths other than `/_ping` and `/version` are under `/v<api>`, where `<api>` is the engine's API version capped at **1.54**, the version the capture was taken at. The minimum is **1.40** (Docker 19.03).

| Request | When | Needed? |
|---|---|---|
| `/_ping`, `/version` | first sync, Test | yes (either one gives the API version) |
| the probe (below) | first sync, then hourly; Test | no: each refusal turns a feature off |
| `/containers/json?all=1` | every sync, every event burst | **yes**: a refusal is a repair (`capability_missing`) |
| `/containers/{id}/json` | per container, only when its summary changed (state, status text or health) or an event names it; 4 at a time | no: without it there are no restart counts, policies or ports of stopped containers |
| `/events?filters=…` | streamed while someone watches | no: without it, polling every 30 s |

**Never read:** logs, archives, exports, exec, attach, networks, volumes, and `system/df` (it can take seconds on a big host). Stats aren't read in v0.1 (below).

**The environment is never kept.** Inspect returns every container's environment. The decoder drops it all, except a socket proxy's own write switches (`POST`, `ALLOW_START`, `ALLOW_STOP`, `ALLOW_RESTARTS`, wollomatic's `SP_ALLOW_*` and `-allow{POST,PUT,PATCH,DELETE}` arguments), which are only read to report grants and are never published. Health-check output (`State.Health.Log`) isn't decoded. Event attributes (every label of the container, secrets included) aren't decoded, and events are never logged.

## The probe: three tiers

Once at the first sync, again every hour, and on Test. Every request is a read-only `GET` about a container named `chadash-probe-0` that doesn't exist, so a request a proxy lets through gets Docker's 400 or 404, never a real container's data. "Docker answered" means not 401/403/405/407 **and** a JSON body. Docker's errors are JSON, while a proxy's refusals (HAProxy's `text/html`, wollomatic's `text/plain`) aren't.

| Request | Means |
|---|---|
| `/swarm`, `/secrets`, `/configs`, `/nodes`, `/plugins` all answered | **raw**: nothing filters. Root on the host for anyone who reaches it |
| else `/containers/chadash-probe-0/logs?stdout=0&stderr=0&tail=0` answered (Docker's 400 "choose at least one stream", returned before it even looks the container up) | **broad-proxy**: a Tecnativa-style `CONTAINERS=1` that lets every GET under `/containers/` through (env, logs, files) |
| else | **filtered-proxy**: a path-anchored allowlist such as the compose preset |
| `/info`, `/images/…/json`, `/containers/…/json`, `/…/stats`, `/events?since=1&until=1` | the optional reads, each on or off |

**A 403 from the proxy is "capability not granted", never an error,** except for the container list, which the integration can't work without.

## What it publishes

IDs follow the wave 5 convention, `docker:<instance id>/<path>`.

**The engine,** `docker:<inst>/engine`, kind `engine`. It's the instance's service resource, named after the instance. Caps `["containers"]`, state `running`, health `ok`.

| Attr | |
|---|---|
| `endpoint` | the configured endpoint (as the Demo Home has it) |
| `via` | `socket-proxy`, `tcp` (a raw API over TCP) or `socket` (the raw socket) |
| `access` | the probe's tier: `raw`, `broad-proxy` or `filtered-proxy` |
| `host`, `ip` | the endpoint's host, and the same as `ip` when it's an address (natural keys for the correlator) |
| `version`, `api_version`, `os`, `arch`, `kernel` | from `/version` |
| `hostname`, `os_name`, `cores`, `mem_gb` | from `/info`, when the proxy allows it |
| `containers`, `running` | counts |
| `manager` | `unraid` when containers carry `net.unraid.docker.managed`, else `compose` when there are compose projects |

**Containers,** `docker:<inst>/container/<name>`, kind `container`. They're keyed by name, which survives a recreate.

| Field | |
|---|---|
| `state` | Docker's: `running`, `exited`, `created`, `restarting`, `paused`, `removing`, `dead` |
| `desired` | `running` when the restart policy is `always` or `unless-stopped`; otherwise unset. Docker can't tell a stop on purpose from an outage with `no` (Unraid's templates) or `on-failure`; the Unraid integration supplies autostart |
| `container_id` | the 12-character short ID, as in the Demo Home. Unraid's `id` (`<server>:<full id>`) matches it by prefix |
| `image` | as listed |
| `ports` | container ports, `"8989/tcp"`, from the inspect (covers stopped containers) or else the list |
| `published` | host bindings, `"18989->8989/tcp"`, with the host IP when it isn't a wildcard (`"127.0.0.1:8080->80/tcp"`): the natural key routes and checks match on |
| `network_mode`, `networks`, `ips` | `ips` are the container's addresses: Traefik's Docker provider routes to bridge IPs |
| `restart_count`, `restart_policy`, `mem_limit_mib` | from the inspect |
| `started_at` (running), `exit_code`, `oom_killed`, `finished_at` (exited) | from the inspect; `exit_code` falls back to the list's `Status` |
| `health_check`, `failing_streak` | Docker's own health check, **only while running**: an exited container keeps a stale status |
| `labels` | the allowlist (below) |
| `links` | `homepage.href` (titled `homepage.name`), and Unraid's `net.unraid.docker.webui` template filled in (`[IP]` → the endpoint host, `[PORT:n]` → the host port bound to `n`, or `n` itself on the host network). The template needs an endpoint host a browser can reach: an IP or a dotted name, not a compose service name like `dockerproxy` or a socket. Only `http(s)` URLs without credentials are kept |

Health:

| Container | Health |
|---|---|
| running, healthy or no health check | `ok`, since it started |
| running, health check `starting` | `attention` |
| running, healthy but `FailingStreak > 0` | `attention` ("health check failed N times in a row") |
| running, `unhealthy` | `down` |
| `restarting` | `attention` |
| exited after an OOM kill, and should run | `down` ("OOM-killed") |
| exited after an OOM kill otherwise | `attention` |
| exited, and should run | `attention` (always and unless-stopped containers Docker didn't restart were usually stopped by hand) |
| exited, restart policy `no` or `on-failure` | `ok`, "stopped (exited N)" |
| `created` | `ok`, "created, never started" |
| `paused`, `removing` | `maintenance` |
| `dead` | `down` |

On the dogfood capture, that makes `llama-swap` `down` and `ai-whisperasr-1` `attention`. The other 19 exited containers are `ok`.

**Stacks,** `docker:<inst>/stack/<project>`, kind `stack` (not in the Demo Home yet), one per `com.docker.compose.project`. Attrs: `project`, `containers`, `running`, `working_dir`, `config_files`, and `managed_by` (`composeman`: Unraid's Compose Manager owns the files on its flash, so they aren't ours to adopt). State is `running` when every container runs, `stopped` when none does, and `partial` otherwise. Health is the worst container's (`attention`, with its name and reason), or `down` when that container is down and nothing in the stack runs.

**Relations:** `container runs_on engine`, `container part_of stack`, `stack runs_on engine`, `integration:<inst> monitors engine`. When the socket proxy serving the endpoint is among the engine's containers, `integration:<inst> depends_on` it too, as in the Demo Home. Matching on an image containing `socket-proxy`, plus the container's name, compose service or published port equal to the endpoint's host or port. Engine `runs_on` host, and `app runs_on container`, are the correlator's (wave 6).

**Metrics:** none in v0.1. The Demo Home's `cpu.pct` and `mem.mib` per container need one stats request per running container per tick (44 on jupiter), and the interest the runtime gives is hub-wide, not per tile. They wait for per-tile interest.

## Label discovery

Homepage builds services from container labels. The importer creates the section those services land in empty (Apps on the dogfood home, discovery entry `label_section`), and adds a `label_endpoint` entry per Docker instance it creates (`"docker": "<instance id>"`). This integration supplies the labels; **wave 6 turns them into tiles**. Nothing here builds tiles.

**Label allowlist.** Only labels under these prefixes are copied: `homepage.`, `chadash.`, `com.docker.compose.`, `net.unraid.docker.`. Never anything else: Traefik's basic-auth labels carry password hashes, and image labels carry maintainers' emails. Within the allowlist, a label is dropped when the segment after its last dot matches `key|pass|secret|token|auth|cookie|credential` (any case: `homepage.widget.key`, `homepage.widget.password`, `homepage.widget.apiKey`), or when its value is a URL with credentials in it. `homepage.widget.username` stays: identifiers are config.

**The shape wave 6 reads.** A discovered service is a resource with:
- `kind: "container"`;
- `source: "integration:<docker instance id>"`, the id in the importer's `label_endpoint` entry;
- both `labels["homepage.group"]` and `labels["homepage.name"]`, which is the condition Homepage itself uses.

```json
{
  "id": "docker:i_mg763up6m3qzsfev/container/immich_server",
  "kind": "container",
  "name": "immich_server",
  "source": "integration:i_mg763up6m3qzsfev",
  "state": "running",
  "health": "ok",
  "labels": {
    "homepage.group": "Apps",
    "homepage.name": "Immich",
    "homepage.href": "https://photos.family.example",
    "homepage.icon": "immich.png",
    "homepage.widget.type": "immich",
    "homepage.widget.url": "http://10.0.11.120:2283",
    "homepage.widget.version": "2",
    "com.docker.compose.project": "immich",
    "com.docker.compose.service": "immich-server",
    "net.unraid.docker.managed": "composeman"
  },
  "attrs": {
    "links": [{"title": "Immich", "url": "https://photos.family.example"}],
    "…": "the container attrs above"
  }
}
```

How the labels map to a tile:

| Label | Tile |
|---|---|
| `homepage.group` | the section whose title matches it, case-insensitively (`Apps` → the importer's `apps` section); a new section when none does |
| `homepage.name` | the tile name (also the link title) |
| `homepage.href` | the tile's link (also the first entry of `attrs.links`) |
| `homepage.icon` | the icon, as Homepage names it (`immich.png`, `mdi-…`, `/icons/…`) |
| `homepage.description` | the subtitle |
| `homepage.weight` | the order within the section, when present |
| `homepage.widget.type` | the catalog widget, as the importer maps Homepage widget types; the generic `service` tile otherwise |
| `homepage.widget.url` and other non-secret `homepage.widget.*` | a candidate instance config. **The widget's secret never arrives** (it's dropped above), so a widget tile needs an instance whose secret the owner enters, or one the importer already created from `services.yaml` |

Homepage's other label forms (`homepage.instance.<name>.*` overrides) arrive verbatim under the same rules. On the dogfood capture, three containers qualify: `immich_server` and `mealie` (group `Apps`) and `proxmox-backup-server` (group `Backup`).

## Events

While someone watches, `Subscribe` opens `/events` with a server-side filter:

```
{"type":["container"],"event":["create","start","restart","die","stop","kill","oom","pause","unpause","destroy","rename","update","health_status"]}
```

That keeps out the health-check `exec_*` events, which were all of the traffic in the capture (40 to 46 a minute). Every (re)connect opens the stream first and then re-lists everything, instead of replaying with `since=`: the engine buffers only 256 events, about five minutes on jupiter. Each burst of events re-lists and re-inspects the containers the events name. A stream that ends is an error, so the runtime reconnects with backoff. HAProxy cuts idle streams, and the response cap (16 MiB) ends a very long-lived one. A refused stream returns the refusal; the runtime retries slowly, and polling carries on.

## Poll plan

| | Interval |
|---|---|
| watched, events connected | 60 s (reconcile: catches what events don't, such as a failing streak) |
| watched, no events | 30 s |
| idle | 5 min |
| timeout | 45 s per sync (the first one inspects every container) |

Each sync is one list request, plus inspects only for containers that changed. A restart between polls changes the list's `Status` text ("Up 5 seconds"), so it's caught without events.

## Grants, repairs and hints

**Test** negotiates, probes and lists, and reports:
- **Grants:** `containers`, plus `inspect`, `events`, `stats`, `info`, `images` when allowed; `logs and files` on a broad proxy.
- **Write grants:** `engine` (`admin`, write) on a raw API or the socket. `writes` (write) when the socket proxy's own switches allow POST or start, stop and restart (`POST=1`, `ALLOW_RESTARTS=1`, wollomatic's `-allowPOST`). Writes are never probed: no request other than a GET is ever sent. A proxy the hub can't see or inspect leaves writes unreported. The runtime warns about any write grant.
- **Warnings:** `docker_raw_api` (critical), `docker_raw_socket` (warning), `docker_broad_proxy` (warning), and `capability_missing` (info), naming the refused reads that cost a feature: events, inspect, info.

**Repairs:**
- a refused container list: `capability_missing`, field `endpoint`;
- an API older than 1.40: `unsupported_version`;
- an endpoint that answers `/_ping` with anything but 200 or a refusal: `invalid_config` ("doesn't look like a Docker API");
- TLS failures, the runtime's usual codes.

**Hints** (quiet Inbox problems, severity `attention`, on the engine; they resolve themselves):

| ID | When |
|---|---|
| `hint.docker_endpoint` | a raw API over TCP ("root on the host"), the raw socket ("full Docker access"), or a broad proxy ("every container's environment, logs and files"). It's the over-broad proxy hint the dogfood home's coarse Tecnativa proxy earns |
| `hint.label_secrets` | containers carry secret-looking labels. Names them (never the values) and notes that Cha Dash doesn't copy them. On the capture: `immich_server`, `mealie` and `proxmox-backup-server` |

## Limits
- No container stats in v0.1 (above).
- No client certificates for TLS endpoints.
- `desired` stays unset for restart policy `no`; the Unraid integration owns autostart.
- Following nginx-proxy hops needs `VIRTUAL_HOST`/`VIRTUAL_PATH`/`VIRTUAL_PORT`, which are environment values. They're dropped like the rest; the correlator (wave 6) decides whether to read them server-side.
- Containers that mount the Docker socket aren't flagged yet.

## Through chadash-node

With `node://<name>`, requests never leave the hub as HTTP: a small transport (`node.go`) turns the integration's GETs into the node's Docker verbs over `Deps.Nodes` (`integration.NodeCaller`): `/_ping` and `/version` → `engine.version`, `/info` → `engine.info`, `/containers/json` → `container.list`, `/containers/{id}/json` → `container.inspect`, `/containers/{id}/stats` → `container.stats`, `/events` → `engine.events` (streamed), and its own `/_chadash/self` → `engine.self` (below). Every other path answers 403, as a filtering proxy would, so the probe turns that feature off. The resources, relations and IDs are the same as through the socket (a test syncs the same engine both ways); like `unix://`, there's no host address to fill Unraid's web-UI links with.

- **The engine** gets `via: node`, `access: node` and `node: <name>`. Test reports a `node` grant and no write grants: what Cha Dash may change is the node's policy's business.
- **A refusal** keeps the integration's meaning: the node's `forbidden` is a proxy's 403 (a feature off, or a Repair when listing is refused, naming the policy), `not_found` is Docker's 404, and a node that isn't connected is a 503 the runtime backs off from.
- **Inspect** leaves the node with environment values redacted (the default `redact_env`), so a socket proxy's write switches read as unknown.
- **Compose `depends_on`** (the `com.docker.compose.depends_on` label) is published as `depends_on` relations between containers of one project, for every endpoint.

## Cha Dash's own container

When the hub runs in a container on this engine, the integration publishes `chadash:hub runs_on docker:<instance>/container/<name>`, so stopping, restarting, recreating or removing that container is the self-aware T4 and its plan lists "Cha Dash itself" (the web's words). It finds the container two ways:

- **Through chadash-node**, the node names it: `engine.self` (permission `docker.read`) answers with the node's own container, its compose project, and the hub's container in that project, the one whose service is `chadash` or, failing that, whose image is a `chadash` image (not `chadash-node`). Exactly one candidate counts. The transport maps it to its own path, `/_chadash/self`. The integration asks hourly, and again at once when the container it named is gone (recreated by an update). A node that predates the verb, or can't tell, answers nothing, and the next rule applies.
- **Through any endpoint**, the hub's own container ID: `integration.HubContainerID` reads it from `/proc/self/mountinfo` (the `/etc/hostname`, `/etc/hosts` and `/etc/resolv.conf` mounts Docker makes from `/var/lib/docker/containers/<id>/`), else from the hostname when it looks like a container ID. A container whose ID matches, at least 12 characters, is the hub's. Exactly one match counts.

A socket proxy on another host never matches, so the relation only appears on the hub's own host. Unraid relates the hub to its container of the same ID too (best effort, `integrations/unraid`).

## Actions through the node

Only `node://` instances implement them (`actions.go`, the `integration.Actor` interface); every other instance's `Actions()` is empty, so a socket proxy never sees a write.

| Action | Tier | Undo | Node verb | Node policy |
|---|---|---|---|---|
| `docker.container.start` | T1 | stop | `container.start` | `docker.lifecycle` |
| `docker.container.stop` | T1 | start | `container.stop` | `docker.lifecycle` |
| `docker.container.restart` | T2 | — | `container.restart` | `docker.lifecycle` |
| `docker.container.recreate` | T2 | — | `container.recreate` | `docker.update` |
| `docker.container.pull` | T2 | — | `container.pull` | `docker.update` |
| `docker.container.remove` | T3 | — | `container.remove` | `docker.remove` |
| `docker.container.logs` | T0 | — | `container.logs` (last 50 lines into the task log) | `docker.logs` |

- **Logs are Operator and up.** Container logs often hold secrets (tokens printed at start-up, connection strings), so `docker.container.logs` is marked `ReadsData`: the policy refuses it to viewers and household although it's T0. Its lines live in the running task and the `tasks` topic only; the stored task never has them.

- **Targets** are this instance's containers, `docker:<instance>/container/<name>`; another instance's are `not_found`. Resolve inspects through the node; the plan's hash covers the container's ID, state and image.
- **Preconditions** refuse a start of a running container, a stop of a stopped one and a remove of a running one, then ask the node (`container.can`) whether its policy allows the verb on that container: a refusal is `409 unavailable` with the node's sentence ("vaultwarden is protected by this node's policy"). A node that isn't connected is `unavailable` too.
- **Blast radius** is the engine's walk of the Home Graph: compose `depends_on`, Traefik's `routes_to`, `part_of` and `runs_on`.
- **Remove** lists the volumes it keeps (named ones always) and the anonymous ones it keeps or deletes (`{"volumes": true}`).
- **Verify** reads the container back through the node until it reports the promised state (by name, so it finds a recreated container).
- **Pull** pulls the container's tag without registry credentials (private images fail with the registry's message); the container runs the old image until it's recreated. Recreate keeps the name, labels, networks and every volume, anonymous ones included, and rolls back if the new container can't be created or started.
