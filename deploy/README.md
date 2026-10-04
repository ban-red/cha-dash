# Deploying Cha Dash (`deploy/`)

Docker Compose is the main way to run Cha Dash ([ADR-0007](../docs/adr/0007-docker-compose-first.md)). Nothing needs configuring: start it, read one token from the logs, and open the page.

| File | What it is |
|---|---|
| [`compose.yaml`](compose.yaml) | The hub, plus an optional read-only Docker socket proxy |
| [`compose.demo.yaml`](compose.demo.yaml) | An override that runs the built-in Demo Home simulator |

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
2. **Start it:**
   ```sh
   docker compose up -d
   ```
3. **Read the setup token** from the logs. The line ends in `setup_token=cds_…`:
   ```sh
   docker compose logs chadash | grep setup
   ```
   A restart before setup prints a fresh token. Use the newest one.
4. **Open `http://<host>:3696`**, enter the token and create the owner account. The token is then spent.

## What runs

| Service | What it does | Hardening |
|---|---|---|
| `chadash` | The hub: UI, API and realtime, on port **3696** | Non-root (uid 65532), read-only root filesystem, no capabilities, `no-new-privileges`. It writes only to `/data` and a 64 MB `/tmp` in memory. The healthcheck is built into the binary |
| `socket-proxy` | Read-only access to this host's Docker API, for the hub only. See [Docker access](#docker-access) | GET and HEAD only, on an internal network with no route out. Read-only root filesystem, no capabilities, 64 MB memory cap |

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
| `CHADASH_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. The setup token is logged at `warn`, so it survives a quiet level |
| `CHADASH_LOG_FORMAT` | `text` | `text` or `json` |
| `CHADASH_DEMO` | off | `1` or `true` runs the Demo Home simulator (flag `--demo`). See [Demo mode](#demo-mode) |
| `CHADASH_DEMO_AT` | `14:05` | Where the demo clock starts: `HH:MM` or `HH:MM:SS` (UTC, on the demo's day), or `now` for the real date and time (flag `--demo-at`) |
| `CHADASH_DEMO_SEED` | `1` | Changes the demo's noise, uptimes and IDs, but not its topology or incidents (flag `--demo-seed`) |
| `CHADASH_DATA_DIR` | `/data` | The data directory. Leave it alone in Docker; mount a volume at `/data` instead |
| `CHADASH_LISTEN` | `:3696` | The address inside the container. Leave it alone in Docker; change the published port instead |

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

## Docker access

The `socket-proxy` service lets Cha Dash **see** the containers on this host. The hub starts reading through it in v0.1; changing anything in Docker is the node's job, later, and never the hub's ([03 · Architecture](../docs/plan/03-architecture.md#how-docker-access-evolves)).

| Allowed | Not allowed |
|---|---|
| `GET` containers (list, inspect, stats), images (list, inspect), `info`, `version`, `events`, networks, volumes; `HEAD /_ping` | Every `POST`, `PUT` and `DELETE`: no start, stop, create or pull. No exec, attach, logs, file copies or exports |

- **Only `chadash` can connect**, by name, over the internal `docker-proxy` network. Nothing is published.
- **The socket is mounted read-only**, and the proxy filters every request by method and path.
- **It runs as uid 0 with every capability dropped.** The socket's group ID differs from host to host, and root inside the container can open a root-owned socket anywhere. Access to the socket is what matters, and the allowlist is what limits it. To run it unprivileged anyway, set `user: "65534:<gid>"`, with the gid from `stat -c %g /var/run/docker.sock`.

**Don't want it?** Switch it off without editing `compose.yaml`:

```yaml
services:
  socket-proxy:
    profiles: [docker]   # only starts with `docker compose --profile docker up -d`
```

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

Then add Docker with the endpoint `tcp://<that host>:2375`. Test connection says which kind of endpoint it found, with GET requests only: a filtered proxy like this one, a broad proxy that also hands out containers' environment, logs and files, or the raw Docker API (root on that host).

## Least-privilege credentials

Cha Dash only reads in v0.1, so every credential it holds should only read. Each integration's form (Settings → Integrations, or the first-run wizard) shows a **recipe** beside the fields: the steps to make a credential with exactly the access Cha Dash needs, with commands to copy, filled in with the names you choose. **Test connection** then reads back what the credential can actually do, where the service can tell, and flags anything beyond reading.

| Integration | The recipe's credential | Can it change anything? |
|---|---|---|
| Proxmox VE | A user, a role with `Datastore.Audit`, `Sys.Audit` and `VM.Audit` (plus `VM.GuestAgent.Audit` on 9.x), and a privilege-separated token, one per instance | No |
| Proxmox Backup Server | A user and its token, both with the Audit role | No |
| Docker | The socket proxy above | No: GET only |
| Unraid | An API key with read access to `info`, `array`, `docker`, `share` and `me` (Unraid API 4.37.4 or later) | No |
| OPNsense | The API key of a user with only *Reporting: Traffic* | No |
| Frigate | A viewer account, on the authenticated port 8971 | No |
| Traefik | Its own basic-auth user on the API, which is read-only anyway | No |
| Immich | An API key with `server.about`, `server.statistics`, `asset.statistics` and `user.read` | No |
| Uptime Kuma | None (a public status page), or a key that only opens `/metrics` | No |
| Built-in checks, Weather | None (Weather's Open-Meteo is keyless) | — |
| Home Assistant, Mealie | A token of a user who isn't an administrator | Yes: the narrowest there is can still control devices (Home Assistant) or edit recipes (Mealie) |
| Plex, Jellyfin, Sonarr, Radarr, Prowlarr, qBittorrent, SABnzbd, Tautulli, Bazarr, Seerr, Pi-hole, AdGuard Home, Linkding, Karakeep | The only kind of key the service has, or a user of its own where that helps revoke it | Yes: none of them offers a read-only credential. Cha Dash still only reads |

**Discovery stays on what you name.** The wizard's Discover step asks the socket proxy this `compose.yaml` runs (`tcp://socket-proxy:2375`) as soon as it opens. Anything else it checks only when you list it: machines by name or address, and Docker addresses you tick. It sends GET requests, without credentials, to the usual ports of the services Cha Dash knows, and never scans a network.

## Behind a reverse proxy

A reverse proxy gives Cha Dash a name and HTTPS. **HTTPS matters:** browsers only allow installing the app (PWA), Web Push and passkeys on a secure origin. Everything else works over plain HTTP on your LAN.

- **Set `CHADASH_TRUSTED_PROXIES`** to the address the hub sees the proxy connect from. Without it, every visitor looks like the proxy: sign-in rate limits are shared, logs show the proxy's IP, and session cookies aren't marked `Secure`.
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

Demo mode runs the **Demo Home simulator**: a made-up homelab with five Proxmox endpoints, an Unraid NAS, Home Assistant, cameras and six scripted incidents. It reads nothing real and needs no Docker access, so the socket proxy stays off.

```sh
curl -fsSLO https://raw.githubusercontent.com/ban-red/cha-dash/main/deploy/compose.demo.yaml
docker compose -f compose.yaml -f compose.demo.yaml up -d
docker compose -f compose.yaml -f compose.demo.yaml logs chadash | grep setup
```

- **It's a separate Compose project** (`chadash-demo`) with its own volume, so demo data never mixes with a real install. Stop a real install first: both use port 3696.
- **The clock starts at 14:05** on the demo's own day (2026-10-03), when all six incidents are open. The day replays from midnight, so the demo never goes quiet. `CHADASH_DEMO_AT: now` follows the real date and time instead.
- **Remove it** with `docker compose -f compose.yaml -f compose.demo.yaml down -v`. That deletes the demo volume only.

## Upgrading

```sh
docker compose pull
docker compose up -d
```

- **Back up first** ([below](#backing-up)). Database migrations run on start, and there's no downgrade: to go back, restore the backup and run the older image.
- **`latest` follows every release.** To upgrade on your own schedule, pin a release tag in `compose.override.yaml`, for example `image: ghcr.io/ban-red/chadash:0.1`.
- **A new `compose.yaml`** can replace the old one; your changes live in `compose.override.yaml`.

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

## Uninstalling

```sh
docker compose down               # removes the containers and networks; keeps your data
docker compose down -v --rmi all  # also deletes the data volume and the images
```

`down -v` deletes the database **and** the vault key. There's no undo, so take a backup first if you might come back.
