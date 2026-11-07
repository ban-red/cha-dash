# Deploying Cha Dash (`deploy/`)

Docker Compose is the main way to run Cha Dash ([ADR-0007](../docs/adr/0007-docker-compose-first.md)). Nothing needs configuring: start it, read one token from the logs, and open the page.

| File | What it is |
|---|---|
| [`compose.yaml`](compose.yaml) | The hub, plus chadash-node for this host's Docker |
| [`compose.socket-proxy.yaml`](compose.socket-proxy.yaml) | An override that swaps the node for a read-only Docker socket proxy (reads only) |
| [`compose.node.yaml`](compose.node.yaml) | chadash-node alone, for another Docker host ([Nodes on other hosts](#nodes-on-other-hosts)) |
| [`compose.demo.yaml`](compose.demo.yaml) | An override that runs the built-in Demo Home simulator |
| [`install.sh`](install.sh) | The one-line installer, attached to each release ([Install](#in-one-line)) |
| [`lxc/`](lxc/README.md) | A draft Proxmox LXC script in the community-scripts style |
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

### In one line

```sh
curl -fsSL https://github.com/ban-red/cha-dash/releases/latest/download/install.sh | sh
```

[`install.sh`](install.sh) checks that Docker and its Compose plugin answer, makes a `chadash` folder, fetches `compose.yaml` from the release's tag (never from `main`) and checks it against the hash the release stamped into the script, checks both images' signatures with [cosign](https://docs.sigstore.dev/) 3 or later when cosign is installed (signed by the release workflow at that exact tag; it says so when cosign isn't there), pins the release in `compose.override.yaml` (by the digest cosign verified, so a tag moved later doesn't change what runs), starts it and prints the address. Then create the owner account straight away (step 3 below). `--dir <folder>` puts it elsewhere, `--no-start` stops before starting, and `--dry-run` prints every step and changes nothing. Running a newer release's `install.sh` in the same folder updates the pin, unless you've edited `compose.override.yaml` yourself (then it says what to change).

**The safer way** is to check the script before it runs. Each release's `install.sh` is listed in its `SHA256SUMS`, which the release workflow signs:

```sh
base=https://github.com/ban-red/cha-dash/releases/latest/download
curl -fsSLO "$base/install.sh"
curl -fsSLO "$base/SHA256SUMS"
curl -fsSLO "$base/SHA256SUMS.cosign.bundle"
cosign verify-blob SHA256SUMS --bundle SHA256SUMS.cosign.bundle \
  --certificate-identity-regexp '^https://github\.com/ban-red/cha-dash/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --ignore-missing -c SHA256SUMS   # install.sh: OK
less install.sh                            # read what it does
sh install.sh
```

On macOS, `shasum -a 256 --ignore-missing -c SHA256SUMS` does the same as `sha256sum`.

### By hand

1. **Fetch the compose file** into a folder of its own:
   ```sh
   mkdir chadash && cd chadash
   curl -fsSLO https://raw.githubusercontent.com/ban-red/cha-dash/main/deploy/compose.yaml
   ```
   It runs `latest`, the newest stable release. To pin a version or follow the betas, see [Upgrading](#upgrading).
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
| `CHADASH_HISTORY` | `standard` | Metrics history to keep in `history.db`: `standard` (48 hours by the minute, 14 days by the quarter hour, tens of MB), `long-term` (also hourly for a year and daily forever) or `off` (nothing on disk; sparklines start empty after a restart). Settings → History changes it at once and wins over this, except that `off` here leaves history out altogether. While history is kept, integrations are read once a minute even with no tab open (5 minutes when off) |
| `CHADASH_HISTORY_DIR` | the data directory | Where `history.db` lives, for example on an SSD when `/data` is on an SD card. Deleting it loses history and nothing else |
| `CHADASH_METRICS_LISTEN` | off | An address for `/metrics` in the Prometheus format, on a listener of its own, such as `127.0.0.1:3697`: every series' current value, each resource's health, and the hub's own memory. Anyone who can reach it reads all of that, so bind it to a private address, or set a token |
| `CHADASH_METRICS_TOKEN` | none | A bearer token Prometheus must send (`authorization: {credentials: …}` in its scrape config). Environment only |
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

`chadash-node` is Cha Dash's hands on a Docker host ([ADR-0006](../docs/adr/0006-owned-stacks-and-thin-node.md)). One node runs beside the hub, on the hub's own host, and pairs by itself. Nodes on other hosts join with a join token from Settings → Nodes → Add node, and wait until an admin approves them ([Nodes on other hosts](#nodes-on-other-hosts)). The hub never mounts the Docker socket: everything Docker goes through the node, and the node only does what its policy allows.

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
stacks: { root: /opt/stacks, allow: [read, deploy, adopt, sync] }   # also: remove, selfupdate (compose.yaml adds selfupdate; see below)
# stacks: { remotes: [git.lab.example, "git.lab.example:2222"] }   # git hosts sync may reach; none listed is any
# stacks: { deny: [privileged, host_namespaces, devices, cap_add, host_binds], bind_roots: [/mnt/media] }
images: { allow: [check] }                 # allow: [] stops the registry checks
# images: { registries: [ghcr.io, "registry.lab.example:5000"] }   # registries the node may log in to; none listed is none
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
| `stacks.read`, `stacks.deploy`, `stacks.adopt` | owned stacks (see stacks). `stacks.deploy` also stops, starts, restarts and takes down a whole stack (down keeps its volumes), and updates a stack's images with its file unchanged: it checks each tag's registry, pulls the tags, recreates the services whose image moved, and rolls back to the old digests if they fail their health check |
| `stacks.remove` | remove a stack: take it down and delete its folder from the stacks root as a commit, so it can be restored from history; its named volumes too, when asked. A stack adopted in place outside the root only stops being managed, and its files stay. Off by default |
| `stacks.sync` | check and fetch the git remote a stack follows, only from the hosts in `stacks.remotes` when it lists any (Settings → Stacks → Follow git) |
| `images.check` | ask each running container's registry whether its image tag has moved, with a `HEAD` request at most every 6 hours, for the Update Center. It changes nothing, but it is traffic from the node's host, so `images: { allow: [] }` turns it off |
| `stacks.selfupdate` | update the hub's own container: Settings → Stacks → Update Cha Dash. Off by default. `compose.yaml` turns it on for the node beside the hub with `CHADASH_NODE_SELFUPDATE: "true"`, which adds it to the default policy; a mounted `policy.yaml` decides alone, so list `selfupdate` there instead. Only a node in the hub's compose project can use it, and it only ever recreates the hub's container |

The hub (`chadash`, found by its compose service or, wherever it runs, by its image) and the node (`chadash-node`) are always protected, whatever the file says: no Docker or stack action restarts or removes them. The one exception is Update Cha Dash (`stacks.selfupdate`), which recreates the hub's container with a new image; the node never touches its own. Protection holds for stacks too: a deploy that would recreate or remove a protected container is refused. A file that doesn't parse, or names an unknown grant, stops the node rather than guess.

**Private registries.** The node pulls from a registry that needs a login, and checks it for updates, with the login from a Docker config file on its host. Log in on the host as usual, then mount that file and list the registry in the policy:

```sh
docker login ghcr.io          # writes ~/.docker/config.json
```

```yaml
# compose.override.yaml
services:
  chadash-node:
    volumes:
      - ~/.docker/config.json:/etc/chadash-node/docker-config.json:ro
```

```yaml
# policy.yaml
images:
  allow: [check]
  registries: [ghcr.io]   # host or host:port; Docker Hub is docker.io
```

The node uses a login only for a registry `images.registries` lists, and sends it only to that registry (a pull, an update check). With no list, the default, it sends none. The login stays on the host: the hub only learns that a registry needs one and whether this node has it ("ghcr.io needs a login, and this node has none for it."). Plain logins work as they are: the `auth` (or `username` and `password`) that `docker login` writes. A `credsStore` or `credHelpers` entry works when its helper, `docker-credential-<name>`, is on the node's `PATH`; the node's image ships none, so a helper means a node image of your own or a node run outside a container. Otherwise the hub says which helper the node couldn't run. Then write a plain file for the node with `docker --config /root/chadash-docker login ghcr.io` and mount `/root/chadash-docker/config.json` instead. The file is read again when it changes. `CHADASH_NODE_DOCKER_CONFIG` puts it elsewhere.

`redact_env` hides environment values only. A container's command line and labels come back as they are, so keep secrets out of them.

**`stacks.deploy` is root on this host.** A compose file can mount the host's disk, run privileged or share the host's namespaces, and Cha Dash deploys what an admin saves. That's by design: media stacks need bind mounts and VPN containers need `cap_add`. The node always refuses what would hand a stack its own keys: the Docker socket, `/`, its own directories and volumes, the `pairing` volume, `volumes_from` another container, env files outside the stack's directory, and `include` or `extends` from other files. Everything else root-equivalent (privileged, host namespaces, devices, `cap_add`, host paths outside the stack's directory) is allowed but called out in the editor and in the deploy confirmation. To lock it down, list those in `stacks.deny`; with `host_binds` denied, `bind_roots` names the host paths stacks may still bind. Or remove `deploy` from `stacks.allow`.

**Its settings** are `CHADASH_NODE_*` variables, none required: `HUB` (`ws://chadash:3696/node`), `NAME` (`local`; `hooks` and `secrets` are taken, and a node that joins with a token can't be `local`, which only local pairing gives), `DIR` (`/etc/chadash-node`), `POLICY`, `PAIRING` (a pairing token, instead of the file; good for one pairing), `PAIRING_FILE` (`/pairing/token`, read again whenever the node pairs), `TOKEN` (a join token, `cdj_…`, for a node on another host), `TOKEN_FILE` (a file holding the join token, such as a Docker secret), `DOCKER_HOST` (`unix:///var/run/docker.sock`), `DOCKER_CONFIG` (`<DIR>/docker-config.json`, the registry logins: see Private registries), `LOG_LEVEL`, `LOG_FORMAT`, `SELFUPDATE` (`false`; `true` adds `stacks.selfupdate` to the default policy, and is ignored when there's a policy file). `TOKEN` and `TOKEN_FILE` are read only until the node has joined; after that the node ignores them and its log says they can be removed.

**Running it unprivileged.** Set `user: "65532:65532"` and `group_add: ["<gid>"]`, with the gid from `stat -c %g /var/run/docker.sock`.

**Revoking it.** Settings → Nodes → Revoke (admin; `POST /api/nodes/<id>/revoke`) cuts it off at once; its key never works again, and the hub writes a fresh pairing secret. To replace a node (say its `node` volume was lost), revoke the old one first: a node with a new key then pairs under the same name with the secret in the file. Pairing never replaces an enrolled node by itself. `POST /api/nodes/pairing/regenerate` (admin) writes a fresh secret at any time.

**Don't want the node?** Start with the socket-proxy override instead. Cha Dash then reads Docker here but can't change anything:

```sh
docker compose -f compose.yaml -f compose.socket-proxy.yaml up -d
```

and add Docker with the endpoint `tcp://socket-proxy:2375`. That proxy allows `GET` on containers (list, inspect, stats), images, `info`, `version` and `events`, only from `chadash`, over an internal network.

**Upgrading from 0.1.** Pull the new `compose.yaml`, then `docker compose up -d --remove-orphans` (the old `socket-proxy` service goes). Edit your Docker instance's endpoint from `tcp://socket-proxy:2375` to `node://local`. To keep the proxy instead, add `-f compose.socket-proxy.yaml` to your commands.

### Nodes on other hosts

[`compose.node.yaml`](compose.node.yaml) runs chadash-node alone, for a Docker host the hub doesn't run on. It joins with a **join token** and waits for an admin to approve it:

1. **Make a token.** In Settings → Nodes → Add node, give the host's name (optional), how long the token works (15 minutes by default, at most 24 hours) and whether approving the node adds its Docker integration (on by default). Cha Dash shows the token once, in a `.env` block with the command to run, each with a Copy button. The hub address in the block is `wss://<host>/node` from `CHADASH_PUBLIC_URL`, else `ws://<the address you opened Cha Dash at>:3696/node`; edit it there if the node reaches the hub another way.
2. **On the other host**, fetch the file into a folder of its own, put the `.env` block next to it and start it:
   ```sh
   curl -fsSLO https://raw.githubusercontent.com/ban-red/cha-dash/main/deploy/compose.node.yaml
   # paste the .env block from Settings → Nodes into .env
   docker compose -f compose.node.yaml up -d
   ```
   It runs `latest`. To pin the node to the hub's version, add `CHADASH_NODE_IMAGE=ghcr.io/ban-red/chadash-node:0.3.0` (say) to `.env`.
3. **Approve it.** The node logs `waiting for approval in Cha Dash; fingerprint …` once, then asks again every 30 seconds for as long as it takes. Settings → Nodes and the Inbox show it waiting, with the address it dialled from, its fingerprint, its version and what its policy allows. Compare the fingerprint with the node's log (`docker compose -f compose.node.yaml logs chadash-node`), then **Approve** it: that asks you to confirm it's you, because an approved node gets stack secrets when Cha Dash deploys there. **Reject** revokes it.
4. **Done.** The node connects on its next try. When the token asked for it, its Docker integration (endpoint `node://<name>`, named after the node) is already there. You can delete the `CHADASH_NODE_TOKEN` line: the token was spent when the node joined, and the node reconnects on its identity in the `node` volume.

What to know:
- **The node dials the hub**, on port 3696 or through your reverse proxy; nothing listens on the node's host. Join tokens are accepted from any address, since a token only makes a node that waits for approval. That's what lets a VPS reach the hub over the internet.
- **A token is single-use and short-lived.** The node that presents it first is enrolled, approved or not; a second node with it is refused, and so is one whose name isn't the token's (when the token names one). A token can't name a node `local`, in any case: that's the hub's own node. An unused token can be revoked in Settings → Nodes; used and expired ones stay listed for 7 days.
- **The node checks the hub's key before it sends the token**, as with pairing: the token carries the hub key to expect, so a hub that can't prove it never sees the secret.
- **A node nobody approves within 7 days** is revoked by the hub, and its Inbox item resolves.
- **Approval doesn't widen anything.** The node's own [local policy](#the-node) still decides what it does. Mount it with the commented line in the file. Its stacks root defaults to `/opt/stacks`, mounted at the same path. To keep stacks elsewhere, set `stacks.root` in the policy and mount that path instead.
- **The pairing secret is for the hub's own host.** Don't copy it to other hosts: it approves a node at once. Nodes that paired that way keep working, and so does `CHADASH_NODE_PAIRING_FROM`.

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
- **Deploy webhooks** from a forge on the internet need the hub reachable from it. Expose only `/api/hooks/` (a path prefix rule on its own router), not the whole hub, or don't expose anything and let syncs poll (every 5 minutes by default). A hook answers nothing but `401` without its signature. GitLab sends its secret as a plain header (`X-Gitlab-Token`), so a proxy that logs request headers would log it: turn header logging off for that path.

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

### The public demo

To host the Demo Home for anyone, add `CHADASH_DEMO_PUBLIC: "true"`. Then:
- **Visitors are signed in automatically** as one operator account, `visitor`, which has no password. There's no owner and no setup page.
- **Only the Demo Home's actions run**, against the simulator. Everything else is refused with a sentence: integrations, imports, specs, nodes, API tokens, users, settings, stacks, notifications, passkeys and board edits.
- **The simulated day restarts every hour**, so every visitor finds the incidents open.
- **New sessions are limited per address.** Behind a reverse proxy, set `CHADASH_TRUSTED_PROXIES` so the hub sees each visitor's address, not the proxy's.
- **The hub refuses to start** with `CHADASH_DEMO_PUBLIC` but without `CHADASH_DEMO`, or on a data directory that already holds integrations. Give it a volume of its own.

A compose file for a small public host, behind a proxy on the same machine:

```yaml
# compose.yaml on the demo host
name: chadash-public-demo

services:
  chadash:
    image: ghcr.io/ban-red/chadash:0.4.0   # pin a release
    restart: unless-stopped
    ports:
      - "127.0.0.1:3696:3696"     # only the proxy reaches it
    volumes:
      - demo:/data
    environment:
      CHADASH_DEMO: "true"
      CHADASH_DEMO_PUBLIC: "true"
      CHADASH_PUBLIC_URL: https://demo.example
      # Where the proxy's requests come from. A proxy on the host reaches a
      # published port from the project network's gateway, such as 172.30.0.1.
      CHADASH_TRUSTED_PROXIES: 172.30.0.0/16
      CHADASH_HISTORY: "off"
    read_only: true
    tmpfs:
      - /tmp:size=64m,mode=1777
    cap_drop: [ALL]
    security_opt:
      - no-new-privileges:true
    mem_limit: 256m

volumes:
  demo:
```

No node, and no Docker socket: the demo reads nothing real. To start it afresh, `docker compose down -v` and `up -d`.

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
- **From Cha Dash itself.** Settings → Stacks → Update Cha Dash takes a tag (`0.3.0`). The node beside the hub pulls it, pins its digest, recreates the hub's container alone and waits up to 2 minutes for its healthcheck. If the check fails, it puts the previous image back on its own. You confirm by typing "Cha Dash" and confirming it's you (T3, a step-up). The page loses the hub for a moment and shows the result when it's back. It changes the running container, not your `compose.yaml`: pin the same tag in `compose.override.yaml` too, or a later `docker compose up -d` goes back to the file's tag. The node itself is still upgraded with `docker compose pull` and `up -d`.

### Checking an image

Every published image is signed by the release workflow with [cosign](https://docs.sigstore.dev/), keyless, and carries build provenance and an SBOM. Checking needs cosign 3 or later: the signatures are stored as OCI referrers, which cosign 2 doesn't find (it says "no signatures found"). To check one before you run it:

```sh
cosign verify ghcr.io/ban-red/chadash:0.2.0-beta.1 \
  --certificate-identity-regexp '^https://github\.com/ban-red/cha-dash/\.github/workflows/release\.yml@refs/tags/v0\.2\.0-beta\.1$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
docker buildx imagetools inspect ghcr.io/ban-red/chadash:0.2.0-beta.1 --format '{{ json .SBOM }}'
```

Name the tag exactly, as above: a pattern ending in `refs/tags/v` would also accept an older release's signed image pushed under a newer tag. The output names the image's digest (`docker-manifest-digest`); pin `image: ghcr.io/ban-red/chadash:<tag>@sha256:<digest>` to run exactly what you checked.

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
