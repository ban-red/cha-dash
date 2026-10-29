# Deploying Cha Dash (`deploy/`)

Docker Compose is the main way to run Cha Dash ([ADR-0007](../docs/adr/0007-docker-compose-first.md)). Nothing needs configuring: start it, read one token from the logs, and open the page.

| File | What it is |
|---|---|
| [`compose.yaml`](compose.yaml) | The hub, plus chadash-node for this host's Docker |
| [`compose.socket-proxy.yaml`](compose.socket-proxy.yaml) | An override that swaps the node for a read-only Docker socket proxy (reads only) |
| [`compose.node.yaml`](compose.node.yaml) | chadash-node alone, for another Docker host ([Nodes on other hosts](#nodes-on-other-hosts)) |
| [`compose.demo.yaml`](compose.demo.yaml) | An override that runs the built-in Demo Home simulator |
| [`moving-from-dockhand.md`](moving-from-dockhand.md) | Moving stacks from Dockhand and Hawser, one host and one stack at a time |
| [`unraid/`](unraid/README.md) | A Community Applications template for Unraid |

Unraid and plain binaries are the secondary channels ([Binaries](#binaries)).

## Hardware

Which platforms get images, and how far they're tested ([ADR-0016](../docs/adr/0016-hardware-floor.md)):

| Tier | Platforms | What it means |
|---|---|---|
| **Supported** | linux/amd64, linux/arm64 | Tested natively in CI. Bugs block a release |
| **Best-effort** | linux/arm/v7 | Images are published and the tests run as 32-bit in CI, but nothing is tested on armv7 hardware, and a regression doesn't hold up a release |
| **Not supported** | armv6 (Pi 1, Zero, Zero W), 386, anything else | No images |

**Memory:** 512 MB of RAM at minimum, OS and Docker included; 1 GB if the host runs other things too. Cha Dash itself stays under about 130 MB at its busiest.

**Raspberry Pi:** on a Pi 3 or newer (3, 4, 5 or Zero 2 W), install a 64-bit OS, and Docker pulls the arm64 image. 32-bit Raspberry Pi OS stops at Docker Engine 28: [Docker's docs](https://docs.docker.com/engine/install/raspberry-pi-os/) name v28 as the last major version with packages for it. Older boards, such as the Pi 2, get the best-effort armv7 image.

## Install

You need Docker with the Compose plugin, on amd64 or arm64.

1. **Fetch the compose file** into a folder of its own:
   ```sh
   mkdir chadash && cd chadash
   curl -fsSLO https://raw.githubusercontent.com/ban-red/cha-dash/main/deploy/compose.yaml
   ```
   **During the beta** there's no `latest` image yet. Follow the betas with an override for both images, as the [beta guide](../docs/beta.md) does:
   ```sh
   printf 'services:\n  chadash:\n    image: ghcr.io/ban-red/chadash:beta\n  chadash-node:\n    image: ghcr.io/ban-red/chadash-node:beta\n' > compose.override.yaml
   ```
2. **Start it:**
   ```sh
   docker compose up -d
   ```
3. **Open `http://<host>:3696`** and create the owner account. The username starts as `admin`; pick a password. Do this straight away: until the owner exists, anyone who can reach the hub can create it.

## What runs

| Service | What it does | Hardening |
|---|---|---|
| `chadash` | The hub: UI, API and realtime, on port **3696** | Non-root (uid 65532), read-only root filesystem, no capabilities, `no-new-privileges`. It writes only to `/data` and a 64 MB `/tmp` in memory. The healthcheck is built into the binary |
| `chadash-node` | This host's Docker, for the hub: reads, logs and actions, within its [local policy](#the-node). It dials the hub on the compose network; it listens on nothing | Read-only root filesystem, no capabilities, `no-new-privileges`, 128 MB memory cap. It runs as uid 0, the Docker socket's owner, because the socket's group differs from host to host; with no capabilities that's access to the socket and nothing more. The hub never gets the socket |

**Your changes go in `compose.override.yaml`** next to `compose.yaml`. Compose loads it automatically, and `compose.yaml` stays as shipped, so you can replace it on upgrade. The examples below are all override files. The `!override` and `!reset` tags in some of them need Compose 2.24 or newer.

## Configuration

Every setting is an environment variable on the `chadash` service. None is required.

```yaml
# compose.override.yaml
services:
  chadash:
    environment:
      CHADASH_LOG_LEVEL: debug
```

| Variable | Default | What it does |
|---|---|---|
| `CHADASH_PUBLIC_URL` | none | The address you open Cha Dash at, as `https://host[:port]` (flag `--public-url`). Passkeys need it, over HTTPS; everything else works without it. It's also trusted like an allowed origin |
| `CHADASH_TRUSTED_PROXIES` | none | Comma-separated IPs or CIDRs of reverse proxies whose `X-Forwarded-For` and `X-Forwarded-Proto` are believed. Needed behind a proxy; see [below](#behind-a-reverse-proxy) |
| `CHADASH_ALLOWED_ORIGINS` | none | Extra `scheme://host[:port]` origins allowed for sign-in, changes and WebSockets. Only for proxies that rewrite the `Host` header. Same-origin always works |
| `CHADASH_SECRET_KEY_FILE` | `/data/secret.key`, generated on first boot | Where the vault key lives. See [Keeping the key outside `/data`](#keeping-the-key-outside-data) |
| `CHADASH_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. The first-run note is logged at `warn`, so it survives a quiet level |
| `CHADASH_LOG_FORMAT` | `text` | `text` or `json` |
| `CHADASH_DEMO` | off | `1` or `true` runs the Demo Home simulator (flag `--demo`). See [Demo mode](#demo-mode) |
| `CHADASH_DEMO_AT` | `local` | The demo clock: `local` starts the scripted day at 14:05 and shows it as the hub's local date and time (set `TZ`); `HH:MM` or `HH:MM:SS` starts at that time on the demo's day, shown in UTC; `now` follows the real date and time in UTC (flag `--demo-at`) |
| `CHADASH_DEMO_SEED` | `1` | Changes the demo's noise, uptimes and IDs, but not its topology or incidents (flag `--demo-seed`) |
| `CHADASH_DATA_DIR` | `/data` | The data directory. Leave it alone in Docker; mount a volume at `/data` instead |
| `CHADASH_LISTEN` | `:3696` | The address inside the container. Leave it alone in Docker; change the published port instead |
| `CHADASH_NODE_PAIRING_FILE` | none (`/pairing/token` in `compose.yaml`) | Turns on pairing for the local node: the hub writes a pairing token there on first run, in the volume it shares with `chadash-node`, and a fresh one each time a node pairs or is revoked. Empty: no node can pair |
| `CHADASH_NODE_PAIRING_FROM` | private and loopback ranges | Comma-separated addresses or CIDRs a node may pair from, or `any`. The compose network is private, so the default fits; an already-paired node connects from anywhere |

**Another port.** Change the published side only, so the container keeps 3696 ([ADR-0009](../docs/adr/0009-default-port-3696.md)). `!override` replaces the list instead of adding to it:

```yaml
services:
  chadash:
    ports: !override
      - "8080:3696"
```

**A folder instead of the named volume.** The hub runs as uid 65532, so give it the folder before the first start:

```sh
mkdir data && sudo chown 65532:65532 data && sudo chmod 700 data
```

```yaml
services:
  chadash:
    volumes: !override
      - ./data:/data
```

### Keeping the key outside `/data`

Optional hardening. With the key elsewhere, a copy of `/data` alone reveals no stored credentials.
- **The file must exist.** When `CHADASH_SECRET_KEY_FILE` is set, the hub never generates a key: a missing file stops it, because that usually means a secret that didn't mount.
- **Format:** 32 random bytes, raw, base64 or hex.
- **Moving an existing install:** use its `/data/secret.key`. A new key can't open secrets sealed with the old one.

```sh
openssl rand -base64 32 > chadash.key
sudo chown 65532:65532 chadash.key && sudo chmod 400 chadash.key
```

```yaml
services:
  chadash:
    environment:
      CHADASH_SECRET_KEY_FILE: /run/secrets/chadash_key
    secrets: [chadash_key]

secrets:
  chadash_key:
    file: ./chadash.key
```

A key file that others can read (Docker Swarm mounts secrets `0444`) is accepted with a warning.

## The node

`chadash-node` is Cha Dash's hands on a Docker host ([ADR-0006](../docs/adr/0006-owned-stacks-and-thin-node.md)). In v0.2 one node runs beside the hub, on the hub's own host, and others can join it by hand ([Nodes on other hosts](#nodes-on-other-hosts)); enrollment from the hub, with approval in the Inbox, comes in v0.3. The hub never mounts the Docker socket: everything Docker goes through the node, and the node only does what its policy allows.

**Pairing is automatic.** On first start the hub writes a pairing token, `cdp_<secret>.<hub key>`, to `/pairing/token` in the `pairing` volume, which only the hub and the node mount. The node reads it, checks the hub's key before it says anything, and presents the secret; the hub enrolls and approves it as `local`. Nothing else on the network can pair: a container without the volume has no secret, and the hub refuses keys it doesn't know. The secret is single-use: pairing spends it and the hub writes a fresh one into the file, so a copy of the volume is worth nothing once the node has paired. Pairing is accepted only from private and loopback addresses (`CHADASH_NODE_PAIRING_FROM` on the hub widens or narrows that), and never under the name of a node that's already enrolled. The channel is mutual TLS 1.3 inside the WebSocket on port 3696, with each side's Ed25519 key pinned ([ADR-0014](../docs/adr/0014-node-transport.md)).

**Then add Docker** with the endpoint `node://local` (the first-run wizard offers it). It reads the same containers and stacks the socket proxy did, and offers actions: start and stop, restart, recreate, pull, remove and logs.

**The local policy** lives at `/etc/chadash-node/policy.yaml`. Without one the node uses the default below. To change it, write the file next to `compose.yaml` and mount it read-only:

```yaml
# compose.override.yaml
services:
  chadash-node:
    volumes:
      - ./policy.yaml:/etc/chadash-node/policy.yaml:ro
```

```yaml
# policy.yaml: the default, spelled out
docker:
  allow: [read, logs, lifecycle, update]   # also: remove, exec (exec needs exec: true too)
  protect: []                              # container or compose service names no write may touch
  redact_env: true                         # inspect leaves without environment values
stacks: { root: /opt/stacks, allow: [read, deploy, adopt] }
# stacks: { deny: [privileged, host_namespaces, devices, cap_add, host_binds], bind_roots: [/mnt/media] }
exec: false
# hub: { pin: "ed25519:…", pin_rotation: false }   # fix the hub's key
```

| Grant | Lets Cha Dash |
|---|---|
| `docker.read` | list and inspect containers, read engine info, events and stats |
| `docker.logs` | stream container logs |
| `docker.lifecycle` | start, stop and restart containers |
| `docker.update` | pull a container's image and recreate it |
| `docker.remove` | remove containers (off by default) |
| `docker.exec` | exec into containers (off by default; needs `exec: true` too) |
| `stacks.read`, `stacks.deploy`, `stacks.adopt` | owned stacks (see stacks) |

The hub (`chadash`) and the node (`chadash-node`) are always protected, whatever the file says: Cha Dash never restarts or removes itself until self-update lands in v0.3. Protection holds for stacks too: a deploy that would recreate or remove a protected container is refused. A file that doesn't parse, or names an unknown grant, stops the node rather than guess.

`redact_env` hides environment values only. A container's command line and labels come back as they are, so keep secrets out of them.

**`stacks.deploy` is root on this host.** A compose file can mount the host's disk, run privileged or share the host's namespaces, and Cha Dash deploys what an admin saves. That's by design: media stacks need bind mounts and VPN containers need `cap_add`. The node always refuses what would hand a stack its own keys: the Docker socket, `/`, its own directories and volumes, the `pairing` volume, `volumes_from` another container, env files outside the stack's directory, and `include` or `extends` from other files. Everything else root-equivalent (privileged, host namespaces, devices, `cap_add`, host paths outside the stack's directory) is allowed but called out in the editor and in the deploy confirmation. To lock it down, list those in `stacks.deny`; with `host_binds` denied, `bind_roots` names the host paths stacks may still bind. Or remove `deploy` from `stacks.allow`.

**Its settings** are `CHADASH_NODE_*` variables, none required: `HUB` (`ws://chadash:3696/node`), `NAME` (`local`), `DIR` (`/etc/chadash-node`), `POLICY`, `PAIRING` (a token, instead of the file; good for one pairing), `PAIRING_FILE` (`/pairing/token`, read again whenever the node pairs), `DOCKER_HOST` (`unix:///var/run/docker.sock`), `LOG_LEVEL`, `LOG_FORMAT`.

**Running it unprivileged.** Set `user: "65532:65532"` and `group_add: ["<gid>"]`, with the gid from `stat -c %g /var/run/docker.sock`.

**Revoking it.** `POST /api/nodes/<id>/revoke` (admin) cuts it off at once; its key never works again, and the hub writes a fresh pairing secret. To replace a node (say its `node` volume was lost), revoke the old one first: a node with a new key then pairs under the same name with the secret in the file. Pairing never replaces an enrolled node by itself. `POST /api/nodes/pairing/regenerate` (admin) writes a fresh secret at any time.

**Don't want the node?** Start with the socket-proxy override instead. Cha Dash then reads Docker here but can't change anything:

```sh
docker compose -f compose.yaml -f compose.socket-proxy.yaml up -d
```

and add Docker with the endpoint `tcp://socket-proxy:2375`. That proxy allows `GET` on containers (list, inspect, stats), images, `info`, `version` and `events`, only from `chadash`, over an internal network.

**Upgrading from 0.1.** Pull the new `compose.yaml`, then `docker compose up -d --remove-orphans` (the old `socket-proxy` service goes). Edit your Docker instance's endpoint from `tcp://socket-proxy:2375` to `node://local`. To keep the proxy instead, add `-f compose.socket-proxy.yaml` to your commands.

### Nodes on other hosts

[`compose.node.yaml`](compose.node.yaml) runs chadash-node alone, for a Docker host the hub doesn't run on. Until v0.3 brings enrollment from the hub, it pairs with the hub's pairing token, passed once:

1. **Read the token** on the hub's host. It's the one the hub writes for its own node; once that node has paired, the file holds a fresh one:
   ```sh
   docker run --rm -v chadash_pairing:/p:ro busybox cat /p/token
   ```
2. **On the other host**, fetch the file into a folder of its own and give it three settings in `.env`:
   ```sh
   curl -fsSLO https://raw.githubusercontent.com/ban-red/cha-dash/main/deploy/compose.node.yaml
   printf 'CHADASH_HUB=ws://<hub host>:3696/node\nCHADASH_NODE_NAME=<this host>\nCHADASH_PAIRING=<token>\n' > .env
   docker compose -f compose.node.yaml up -d
   ```
   **During the beta** there's no `latest` image. Add `CHADASH_NODE_IMAGE=ghcr.io/ban-red/chadash-node:beta` to `.env` before starting it, or a pinned tag such as `0.2.0-beta.1`.
3. **Check Settings** lists the node, then **delete the `CHADASH_PAIRING` line**. The token was spent when the node paired, so the line is worth nothing; the node reconnects on its identity in the `node` volume.
4. **Add Docker** with the endpoint `node://<this host>`.

What to know:
- **The node dials the hub**, on port 3696; nothing listens on the node's host. The hub must be reachable from it, and it accepts pairing only from private and loopback addresses (`CHADASH_NODE_PAIRING_FROM`).
- **A token pairs one node, under the name it asks for.** It's refused for a name already enrolled; revoke the old node first.
- **Treat the token like a password until it's spent:** whoever presents it first becomes a node, approved at once. v0.3's join tokens add an approval in the Inbox.
- The same [local policy](#the-node) applies. Mount it with the commented line in the file. Its stacks root defaults to `/opt/stacks`, mounted at the same path. To keep stacks elsewhere, set `stacks.root` in the policy and mount that path instead.

### Docker on another host

Run the same read-only proxy there, published on a port and limited to the hub's address. Settings → Integrations → Docker generates this with your names filled in; replace `192.0.2.10` with the address the Docker host sees Cha Dash connect from.

```yaml
# compose.yaml on the other Docker host
services:
  socket-proxy:
    image: wollomatic/socket-proxy:1.13.1@sha256:3935b709275e4ec35d6ed5a5c4a1f0d01ed31eec5e7234efc3357ecd47689002
    restart: unless-stopped
    user: "0:0"
    read_only: true
    cap_drop: [ALL]
    security_opt:
      - no-new-privileges:true
    mem_limit: 64m
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
    ports:
      # Better: publish on one interface only, as "<this host's LAN IP>:2375:2375".
      - "2375:2375"
    command:
      - -listenip=0.0.0.0
      - -allowfrom=192.0.2.10
      - -allowHEAD=/(v1\.[0-9]+/)?_ping
      - -allowGET=/(v1\.[0-9]+/)?(_ping|version|info|events)
      - -allowGET=/(v1\.[0-9]+/)?containers/json
      - -allowGET=/(v1\.[0-9]+/)?containers/[a-zA-Z0-9][a-zA-Z0-9_.-]*/(json|stats)
      - -allowGET=/(v1\.[0-9]+/)?images/json
      - -allowGET=/(v1\.[0-9]+/)?images/[a-zA-Z0-9][a-zA-Z0-9_.:/@-]*/json
      - -watchdoginterval=3600
      - -stoponwatchdog
      - -shutdowngracetime=5
```

`-allowfrom` checks only the source address. The allowlist lets container inspects through, and an inspect includes the container's environment variables: Cha Dash drops them, but anything else that connects from the hub's address gets them too. On a bridge network, that's every container on the hub's host. Prefer a VPN or a dedicated interface between the hosts. To give up inspects, change `(json|stats)` to `stats`: you keep the container list and state, but lose restarts, exit codes, health and out-of-memory kills.

Then add Docker with the endpoint `tcp://<that host>:2375`. Test connection says which kind of endpoint it found, with GET requests only: a filtered proxy like this one, a broad proxy that also hands out containers' environment, logs and files, or the raw Docker API (root on that host).

## Least-privilege credentials

Cha Dash only reads in v0.1, so every credential it holds should only read. Each integration's form (Settings → Integrations, or the first-run wizard) shows a **recipe** beside the fields: the steps to make a credential with exactly the access Cha Dash needs, with commands to copy, filled in with the names you choose. **Test connection** then reads back what the credential can actually do, where the service can tell, and flags anything beyond reading.

| Integration | The recipe's credential | Can it change anything? |
|---|---|---|
| Proxmox VE | A user, a role with `Datastore.Audit`, `Sys.Audit` and `VM.Audit` (plus `VM.GuestAgent.Audit` on 9.x), and a privilege-separated token, one per instance | No |
| Proxmox Backup Server | A user and its token, both with the Audit role | No |
| Docker | chadash-node, within its local policy ([The node](#the-node)); or a read-only socket proxy | Through the node, only what its policy grants; through a proxy, no (GET only) |
| Unraid | An API key with read access to `info`, `array`, `docker`, `share` and `me` (Unraid API 4.37.4 or later) | No |
| OPNsense | The API key of a user with only *Reporting: Traffic* | No |
| Frigate | A viewer account, on the authenticated port 8971 | No |
| Traefik | Its own basic-auth user on the API, which is read-only anyway | No |
| Immich | An API key with `server.about`, `server.statistics`, `asset.statistics` and `user.read` | No |
| Uptime Kuma | None (a public status page), or a key that only opens `/metrics` | No |
| Built-in checks, Weather | None (Weather's Open-Meteo is keyless) | — |
| Home Assistant, Mealie | A token of a user who isn't an administrator | Yes: the narrowest there is can still control devices (Home Assistant) or edit recipes (Mealie) |
| Nextcloud | A serverinfo token, which reads only the monitoring endpoint (or an administrator's app password) | No, with the token |
| Paperless-ngx | The token of a user with view permissions only | No |
| Speedtest Tracker | An API token with only the `results:read` ability | No |
| Audiobookshelf | The API key of a user with no upload, change or delete permission (open sessions then stay unknown; they need an admin) | No |
| NZBGet | The restricted user, which can't see or change settings | Yes: it can still pause and delete downloads |
| Plex, Jellyfin, Sonarr, Radarr, Lidarr, Prowlarr, qBittorrent, Transmission, SABnzbd, Tautulli, Bazarr, Seerr (Overseerr, Jellyseerr), Pi-hole, AdGuard Home, Linkding, Karakeep | The only kind of key the service has, or a user of its own where that helps revoke it | Yes: none of them offers a read-only credential. Cha Dash still only reads |

**Discovery stays on what you name.** The wizard's Discover step asks the socket proxy this `compose.yaml` runs (`tcp://socket-proxy:2375`) as soon as it opens. Anything else it checks only when you list it: machines by name or address, and Docker addresses you tick. It sends GET requests, without credentials, to the usual ports of the services Cha Dash knows, and never scans a network.

## Behind a reverse proxy

A reverse proxy gives Cha Dash a name and HTTPS. **HTTPS matters:** browsers only allow installing the app (PWA), Web Push and passkeys on a secure origin. Everything else works over plain HTTP on your LAN.

- **Set `CHADASH_TRUSTED_PROXIES`** to the address the hub sees the proxy connect from. Without it, every visitor looks like the proxy: sign-in rate limits are shared, five wrong passwords from anyone ban the proxy's address and so everyone, logs show the proxy's IP, and session cookies aren't marked `Secure`.
- **Banned yourself?** Five wrong passwords in a row from one address ban it until it's cleared. An admin clears it in Settings → Users; if nobody can sign in, run `docker compose exec chadash /chadash bans` to list bans and `docker compose exec chadash /chadash bans clear <ip>` to lift one.
  - Proxy in Docker: the subnet of the network it shares with `chadash`. This prints it:
    `docker network inspect proxy --format '{{range .IPAM.Config}}{{.Subnet}}{{end}}'`
  - Proxy on the host, pointing at `127.0.0.1:3696`: the gateway of the `chadash_default` network:
    `docker network inspect chadash_default --format '{{range .IPAM.Config}}{{.Gateway}}{{end}}'`
- **Set `CHADASH_PUBLIC_URL`** to the address you open Cha Dash at, e.g. `https://dash.lab.example`. Passkeys are tied to that name, so changing it later means enrolling them again.
- **WebSockets** (`/ws`) pass through Traefik and Caddy with no extra settings.
- **`CHADASH_ALLOWED_ORIGINS`** is only needed if your proxy rewrites the `Host` header. Neither example below does.

Both examples assume the proxy runs in Docker, on an existing network called `proxy`, and the hub gets the name `dash.lab.example`.

### Traefik

```yaml
# compose.override.yaml
services:
  chadash:
    ports: !reset []                       # only reachable through Traefik
    networks: [proxy]
    environment:
      CHADASH_TRUSTED_PROXIES: 172.30.0.0/16   # the proxy network's subnet
      CHADASH_PUBLIC_URL: https://dash.lab.example
    labels:
      traefik.enable: "true"
      traefik.docker.network: proxy
      traefik.http.routers.chadash.rule: Host(`dash.lab.example`)
      traefik.http.routers.chadash.entrypoints: websecure
      traefik.http.routers.chadash.tls.certresolver: letsencrypt
      traefik.http.services.chadash.loadbalancer.server.port: "3696"

networks:
  proxy:
    external: true
```

Use your own entrypoint and certificate resolver names.

### Caddy

```
# Caddyfile
dash.lab.example {
	reverse_proxy chadash:3696
}
```

```yaml
# compose.override.yaml
services:
  chadash:
    ports: !reset []
    networks: [proxy]
    environment:
      CHADASH_TRUSTED_PROXIES: 172.30.0.0/16
      CHADASH_PUBLIC_URL: https://dash.lab.example

networks:
  proxy:
    external: true
```

Caddy gets the certificate and sets the forwarded headers on its own.

## Demo mode

Demo mode runs the **Demo Home simulator**: a made-up homelab with five Proxmox endpoints, an Unraid NAS, Home Assistant, cameras and six scripted incidents. It reads nothing real and needs no Docker access, so the node stays off.

```sh
curl -fsSLO https://raw.githubusercontent.com/ban-red/cha-dash/main/deploy/compose.demo.yaml
docker compose -f compose.yaml -f compose.demo.yaml up -d
docker compose -f compose.yaml -f compose.demo.yaml logs chadash | grep setup
```

- **It's a separate Compose project** (`chadash-demo`) with its own volume, so demo data never mixes with a real install. Stop a real install first: both use port 3696.
- **The clock shows your local time and date**, with the scripted day starting at its busiest moment (every incident open) when the hub starts; incident times are shifted to match. Set `TZ` (for example `TZ: Europe/London`) so the container knows your zone; without it, that's UTC. The day replays every 24 hours, so the demo never goes quiet. `CHADASH_DEMO_AT: now` plays the script at its own hours in UTC instead.
- **Remove it** with `docker compose -f compose.yaml -f compose.demo.yaml down -v`. That deletes the demo volume only.

## Upgrading

```sh
docker compose pull
docker compose up -d
```

- **Back up first** ([below](#backing-up)). Database migrations run on start, and there's no downgrade: to go back, restore the backup and run the older image.
- **Image tags.** To upgrade on your own schedule, pin one in `compose.override.yaml`:

  | Tag | Follows |
  |---|---|
  | `latest` | Every stable release |
  | `0.2` | Stable releases in the 0.2 line: fixes, nothing that needs you to change anything |
  | `0.2.0`, `0.2.0-beta.1` | Exactly that release |
  | `beta` | The newest beta |

- **A new `compose.yaml`** can replace the old one; your changes live in `compose.override.yaml`.

### Checking an image

Every published image is signed by the release workflow with [cosign](https://docs.sigstore.dev/), keyless, and carries build provenance and an SBOM. To check one before you run it:

```sh
cosign verify ghcr.io/ban-red/chadash:0.2.0-beta.1 \
  --certificate-identity-regexp '^https://github\.com/ban-red/cha-dash/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
docker buildx imagetools inspect ghcr.io/ban-red/chadash:0.2.0-beta.1 --format '{{ json .SBOM }}'
```

## Backing up

Everything lives in the `/data` volume (`chadash_data`):

| File | What it holds |
|---|---|
| `chadash.db`, plus `-wal` and `-shm` | Boards, layouts, users, sessions, settings, and the stored credentials, encrypted |
| `secret.key` | The vault key that decrypts those credentials |

**The database and the key belong together.** The key is the only way to open the stored credentials: lose it and they are gone for good. Back up both, and restore both.

**Files are private to the hub's user.** The hub runs with umask 077, so everything it writes in `/data` is mode 0600 (owner 65532). A backup tool running as another user needs to run as 65532 or as root, or copy the volume from the host.

Stop the hub for a consistent copy, then archive the volume:

```sh
docker compose stop chadash
docker run --rm -v chadash_data:/data:ro -v "$PWD":/backup alpine \
  tar czf /backup/chadash-backup.tar.gz -C /data .
docker compose start chadash
```

To restore, stop the hub, unpack into the volume, and give the files back to uid 65532:

```sh
docker compose stop chadash
docker run --rm -v chadash_data:/data -v "$PWD":/backup alpine \
  sh -c 'tar xzf /backup/chadash-backup.tar.gz -C /data && chown -R 65532:65532 /data && chmod 700 /data'
docker compose start chadash
```

**Treat the archive as a secret.** With the key inside, it opens every stored credential. Keep it encrypted, or move the key out of `/data` ([how](#keeping-the-key-outside-data)) and back the two up separately.

## Binaries

Every [release](https://github.com/ban-red/cha-dash/releases) also has the hub as a single static binary: `chadash-<version>-linux-amd64`, `-linux-arm64` and `-linux-armv7`, plus `-darwin-arm64` and `-darwin-amd64` for development on a Mac. The web app is inside, so there's nothing else to install. `SHA256SUMS` lists their checksums, and `SHA256SUMS.cosign.bundle` is its signature.

```sh
v=0.2.0-beta.1
curl -fsSLO "https://github.com/ban-red/cha-dash/releases/download/v$v/chadash-$v-linux-amd64"
curl -fsSLO "https://github.com/ban-red/cha-dash/releases/download/v$v/SHA256SUMS"
sha256sum --ignore-missing -c SHA256SUMS
sudo install -m 0755 "chadash-$v-linux-amd64" /usr/local/bin/chadash
```

It takes the same `CHADASH_*` settings as the container; give it a data directory of its own. With systemd, a unit like this runs it as a throwaway unprivileged user that can write only to `/var/lib/chadash`:

```ini
# /etc/systemd/system/chadash.service
[Unit]
Description=Cha Dash hub
Wants=network-online.target
After=network-online.target

[Service]
ExecStart=/usr/local/bin/chadash serve
Environment=CHADASH_DATA_DIR=/var/lib/chadash
DynamicUser=yes
StateDirectory=chadash
StateDirectoryMode=0700
CapabilityBoundingSet=
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl enable --now chadash
journalctl -u chadash | grep setup
```

Docker access works through a socket proxy, as with Compose. To upgrade, back up `/var/lib/chadash`, replace the binary and restart the service.

## Uninstalling

```sh
docker compose down               # removes the containers and networks; keeps your data
docker compose down -v --rmi all  # also deletes the data volume and the images
```

`down -v` deletes the database **and** the vault key. There's no undo, so take a backup first if you might come back.
