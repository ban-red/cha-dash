# Spike 4.3 · Traefik routers → service URLs → containers

Throwaway code that answers one question: **what does the v0.1 Traefik integration need to turn routers into `routes_to` / `exposes` relations and "open URL" links**, including path-based routes like `/sonarr`? It reads the dogfood home's Traefik API read-only, records sanitized fixtures ([`testdata/integrations/traefik/`](../../testdata/integrations/traefik/README.md)), and correlates them with the Docker fixture from [spike 4.4](../docker-proxy/README.md).

Not product code. The findings go into the notes below. The real integration is written fresh.

## How it works

| Command | What it does |
|---|---|
| `capture` | Reads `/api/version`, `/api/overview`, `/api/entrypoints`, and HTTP/TCP/UDP routers, services and middlewares (paginated). Also sends two **unauthenticated** GETs (`/api/version`, `/api/rawdata`) to check whether the API is behind auth. Status codes only for those |
| `sanitize` | Turns the raw capture into fixtures. Domains, addresses, hashes and IDs are rewritten (rules in the fixture README). It writes the private **leak list** of every real value it saw |
| `leakcheck` | Scans directories for every leak-list value (prints file:line and kind, never the value), plus list-free invariants. It first plants a known canary and requires the scanner to find it |
| `analyze` | Runs on the **sanitized** fixtures only, so its output is safe to print: routers → services → backends → containers |

The shared plumbing lives in [`labkit/`](labkit). [`spikes/docker-proxy`](../docker-proxy) reuses it through a `replace` directive.

**Safety, enforced in code:**
- The HTTP client refuses every method except GET and HEAD. It also refuses requests with a body and never follows redirects, so a 3xx can't carry the basic-auth header elsewhere. Covered by `TestReadOnlyClientRefusesWrites`.
- Requests are sequential and paced at ≥300 ms, with 15 s timeouts.
- Credentials are read at runtime from the git-ignored env file (`--env`). Error messages are reduced to their kind (`labkit.SafeErr`), so URLs and hosts never reach a log.
- TLS uses trust-on-first-use. The system roots are tried first, and a self-signed leaf is pinned by SHA-256 in a private file. It wasn't needed here: the dashboard URL is plain `http://`.

## Run it

```bash
cd spikes/traefik
LAB=/somewhere/outside/the/repo    # raw capture, state and leak list hold real values
go run . capture  --env ../../.env.local --raw $LAB/traefik-raw --pins $LAB/tofu-pins.json
go run . sanitize --env ../../.env.local --raw $LAB/traefik-raw --state $LAB/state.json \
                  --leak $LAB/leak-values.json --out ../../testdata/integrations/traefik
go run . leakcheck --values $LAB/leak-values.json ../../testdata/integrations/traefik
go run . analyze  --fixtures ../../testdata/integrations/traefik --docker ../../testdata/integrations/docker/jupiter
LEAKCHECK_VALUES=$LAB/leak-values.json go test ./...   # without the variable the leak test skips; the invariants still run
```

Run the Traefik sanitize before the Docker one. They share `state.json` (salt and domain map), so both fixtures use the same fakes.

## Findings

Captured on 2026-10-04 from the dogfood home's Traefik on pvedocker: **v3.6.7, providers Docker + File, 61 HTTP routers / 56 services / 7 middlewares** (the numbers Homepage's widget shows), 3 TCP routers, no UDP.

### 1. Where routes come from
- **56 of 61 routers come from the File provider.** Only 5 come from Docker labels (Authelia, Homepage, IT-Tools, Linkding, Uptime Kuma, all on pvedocker). jupiter's containers carry **no `traefik.*` labels at all**: everything on jupiter is routed by hand in the dynamic config, to `http://<jupiter IP>:<host port>`. **The correlator can't rely on Docker labels.** It has to resolve backend URLs.
- **Backends, by kind:** 30 → jupiter by IP and host port; 19 → other LAN hosts (OPNsense, five PVE UIs, HA, pikvm, …); 5 → container IPs on pvedocker's bridge network (Docker provider); 2 → host names (one public, one `.lan`); 5 → `@internal` (API, dashboard, a `noop` redirect).
- A router's `service` has no provider suffix when the service lives in the same provider (`"service": "sonarr"` means `sonarr@file`). Qualify it with the router's provider before looking it up.

### 2. Path-based routes (`/sonarr`)
- 7 app routes share `Host(jupiter.lab.example)` with a `PathPrefix`: `/sonarr`, `/radarr`, `/radarrhd`, `/bazarr`, `/prowlarr`, `/tautulli`, `/jackett`. They have **explicit priority 999**, above the catch-alls `jupiter@file` (`PathPrefix(/)`, 998) and `jupiter_base` (`Path(/)`, 997), which send everything else to the Unraid web UI.
- **No stripPrefix is in use.** Two stripPrefix middlewares exist (`strip-jupiter-prefixes` lists `/sonarr /radarr /bazarr /tautulli`) but no router uses them. The apps run with a URL base of `/sonarr` etc. So open URL = `https://<host>/<prefix>`, and an API client must use the same base path.
- **Two kinds of path backends:**
  - **Direct:** `/bazarr` → `10.0.11.120:6767`, `/prowlarr` → `:9696`, `/tautulli` → `:8181`. These are the containers' published ports.
  - **Through a second reverse proxy:** `/sonarr`, `/radarr` and `/radarrhd` go to `http://10.0.11.120:7780/<path>`. The server URL carries the path, and `:7780` is the **`nginxproxy` container** (nginx-proxy). It picks the app from the target containers' `VIRTUAL_HOST` / `VIRTUAL_PATH` / `VIRTUAL_PORT` / `VIRTUAL_HOST_MULTIPORTS` env vars. Which radarr serves `/radarrhd` (`radarr` or `radarr-uhd`) is only answerable from those env *values*.
- All 7 app path routes sit behind **Authelia forwardAuth**. A tile's "open" link should use the routed URL. The integration's API base URL must be the backend (`http://10.0.11.120:6767`), not the SSO-protected route.

### 3. Correlation with jupiter's containers (host port → container)

From `analyze` with the Docker fixture (`HostConfig.PortBindings`, which includes stopped containers, plus host-network `ExposedPorts`):

| Outcome | Count |
|---|---|
| Running container | 19 (3 of them via nginx-proxy) |
| **Stopped container** (`BookLore` created, `Tachidesk-Docker`, `pingvin-share`, `RomM` exited) | 4 |
| The Unraid UI itself (:80) | 2 |
| No container binds the port (a VM, or a removed container: `jackett`, `nextcloud`, `paste`, `picsur`, `tubearchivist`) | 5 |

- **`serverStatus` is `UP` for all of them, including the 9 with nothing running behind.** No service defines a `healthCheck`, so Traefik reports every server as `UP`. **Never treat `serverStatus` as health** unless the service has a health check. Use the hub's own HTTP check, or the backend container's state.
- **"This route points at a stopped container"** (4 here) and **"this route points at nothing"** (5) are cheap, useful Inbox hints. The first is also a root-cause hint when the route's HTTP check fails.
- The Docker-provider routes go to bridge IPs on pvedocker (`10.106.0.x`). Resolving them needs pvedocker's Docker endpoint, which wasn't reachable from this Mac (see 4.4): match the IP against `NetworkSettings.Networks[*].IPAddress`. The router name also equals the compose service name here (`linkding@docker`).
- **Karakeep has no Traefik router at all.** The Homepage README expects it to be linked "through Traefik routes and published ports", so for Karakeep only the published port (pvedocker) or the Homepage href can work.
- **The `family.example` hrefs on jupiter's containers** (Immich, Mealie `homepage.href`) have **no matching Traefik router**. Immich is routed as `photos.lab.example`, and Mealie isn't routed at all. That domain is served by something else (another proxy or a tunnel). The correlator must not assume every href host is a Traefik route.

Full table (sanitized; `go run . analyze …` regenerates it):

| Router | Open URL | Middlewares | Backend | Resolves to |
|---|---|---|---|---|
| `authelia@docker` | https://authelia.lab.example | →https | `http://10.106.0.4:9091` | container on Traefik's own Docker host (pvedocker), docker provider |
| `homepage@docker` | https://dash.lab.example | →https | `http://10.106.0.6:3000` | container on Traefik's own Docker host (pvedocker), docker provider |
| `ittools@docker` | https://tools.lab.example | →https | `http://10.106.0.9:80` | container on Traefik's own Docker host (pvedocker), docker provider |
| `linkding@docker` | https://links.lab.example | →https | `http://10.106.0.5:9090` | container on Traefik's own Docker host (pvedocker), docker provider |
| `uptimekuma@docker` | https://uptime.lab.example | →https | `http://10.106.0.7:3001` | container on Traefik's own Docker host (pvedocker), docker provider |
| `api@file` | http://traefik.home1.lan/api | auth:auth | `api@internal` |  |
| `plex-rewrite@file` | https://app.plex.tv:80 | redirectregex | `noop@internal` |  |
| `traefik-local@file` | http://traefik.home1.lan |  | `api@internal` |  |
| `traefik@file` | https://traefik.lab.example | →https | `api@internal` |  |
| `websecure-api@file` | https://traefik.home1.lan/api | auth:auth | `api@internal` |  |
| `audiobookshelf@file` | https://audiobooks.lab.example | →https | `http://10.0.11.120:61080` | jupiter → audiobookshelf (ghcr.io/advplyr/audiobookshelf) |
| `bazarr@file` | https://jupiter.lab.example/bazarr | →https auth:authelia | `http://10.0.11.120:6767` | jupiter → bazarr (lscr.io/linuxserver/bazarr:latest) |
| `booklore@file` | https://books.lab.example | →https | `http://10.0.11.120:6060` | jupiter → BookLore [created] (ghcr.io/booklore-app/booklore:latest) |
| `coder@file` | https://code.lab.example | →https | `http://10.0.11.120:7080` | jupiter → coder (ghcr.io/coder/coder:latest) |
| `dbgate@file` | https://db.lab.example | →https | `http://10.0.11.120:3001` | jupiter → dbgate (dbgate/dbgate:latest) |
| `host2-local@file` | https://host2.local:80 |  | `http://10.0.11.120:3000` | jupiter → outline (outlinewiki/outline:latest) |
| `immich@file` | https://photos.lab.example | →https | `http://10.0.11.120:2283` | jupiter → immich_server (ghcr.io/immich-app/immich-server:v3) |
| `jackett@file` | https://jupiter.lab.example/jackett | →https auth:authelia | `http://10.0.11.120:9117` | jupiter :9117 (no container binds it: a VM, or gone) |
| `jupiter@file` | https://jupiter.lab.example | →https | `http://10.0.11.120:80` | jupiter itself (Unraid web UI) |
| `jupiter_base@file` | https://jupiter.lab.example | →https | `http://10.0.11.120:80` | jupiter itself (Unraid web UI) |
| `manga@file` | https://manga.lab.example | auth:authelia | `http://10.0.11.120:4567` | jupiter → Tachidesk-Docker [exited] (ghcr.io/suwayomi/tachidesk:preview) |
| `nextcloud@file` | https://cloud.lab.example | →https | `http://10.0.11.120:8688` | jupiter :8688 (no container binds it: a VM, or gone) |
| `onedev@file` | https://onedev.lab.example | →https | `http://10.0.11.120:6610` | jupiter → Onedev (1dev/server) |
| `outline@file` | https://docs.lab.example | →https | `http://10.0.11.120:3000` | jupiter → outline (outlinewiki/outline:latest) |
| `pairdrop@file` | https://drop.lab.example | →https | `http://10.0.11.120:3300` | jupiter → pairdrop (lscr.io/linuxserver/pairdrop) |
| `paperless@file` | https://paperless.lab.example | →https | `http://10.0.11.120:8000` | jupiter → paperless-ngx (ghcr.io/paperless-ngx/paperless-ngx) |
| `paste@file` | https://host3.local:80 |  | `http://10.0.11.120:50009` | jupiter :50009 (no container binds it: a VM, or gone) |
| `pdf@file` | https://pdf.lab.example | →https | `http://10.0.11.120:8282` | jupiter → Stirling-PDF (frooodle/s-pdf) |
| `picsur@file` | https://img.lab.example | →https | `http://10.0.11.120:8187` | jupiter :8187 (no container binds it: a VM, or gone) |
| `pingvin-share@file` | https://share.lab.example | →https | `http://10.0.11.120:3301` | jupiter → pingvin-share [exited] (stonith404/pingvin-share) |
| `plex@file` | https://plex.lab.example | →https | `http://10.0.11.120:32400` | jupiter → Plex-Current (host network) (lscr.io/linuxserver/plex:latest) |
| `prowlarr@file` | https://jupiter.lab.example/prowlarr | →https auth:authelia | `http://10.0.11.120:9696` | jupiter → binhex-prowlarr (binhex/arch-prowlarr) |
| `qbittorent@file` | https://torrents.lab.example | →https | `http://10.0.11.120:8112` | jupiter → binhex-qbittorrentvpn (binhex/arch-qbittorrentvpn:latest) |
| `radarr@file` | https://jupiter.lab.example/radarr | →https auth:authelia | `http://10.0.11.120:7780/radarr` | jupiter → nginxproxy (nginxproxy/nginx-proxy:alpine), backend path /radarr |
| `radarrhd@file` | https://jupiter.lab.example/radarrhd | →https auth:authelia | `http://10.0.11.120:7780/radarrhd` | jupiter → nginxproxy (nginxproxy/nginx-proxy:alpine), backend path /radarrhd |
| `reddit@file` | https://reddit.lab.example | →https | `http://10.0.11.120:8380` | jupiter → redlib (ghcr.io/cycneuramus/containers:redlib) |
| `romm@file` | https://roms.lab.example | →https | `http://10.0.11.120:8100` | jupiter → RomM [exited] (rommapp/romm:latest) |
| `sonarr@file` | https://jupiter.lab.example/sonarr | →https auth:authelia | `http://10.0.11.120:7780/sonarr` | jupiter → nginxproxy (nginxproxy/nginx-proxy:alpine), backend path /sonarr |
| `tautulli@file` | https://jupiter.lab.example/tautulli | →https auth:authelia | `http://10.0.11.120:8181` | jupiter → tautulli (lscr.io/linuxserver/tautulli:latest) |
| `tubearchivist@file` | https://tubearchivist.lab.example | →https | `http://10.0.11.120:18000` | jupiter :18000 (no container binds it: a VM, or gone) |
| `adguard@file` | https://adg.lab.example | →https | `http://10.0.11.1:81` | LAN host .1 |
| `donetick@file` | https://todo.lab.example | →https | `http://10.0.11.201:2021` | LAN host .201 |
| `erebor@file` | https://erebor.lab.example | →https | `https://10.0.11.44:8006` | LAN host .44 |
| `europa-kasm@file` | https://europa.lab.example | →https | `https://10.0.11.181` | LAN host .181 |
| `homeassistant-local@file` | https://host1.local:80 |  | `http://10.0.11.20:8123` | LAN host .20 |
| `homeassistant@file` | https://home.lab.example | headers →https | `http://10.0.11.20:8123` | LAN host .20 |
| `jellyfinone@file` | https://jellyfinone.lab.example | →https | `http://10.0.11.202:8096` | LAN host .202 |
| `omada@file` | https://omada.lab.example | →https | `https://10.0.11.201:18043` | LAN host .201 |
| `omv@file` | https://omv.lab.example | →https | `http://10.0.11.203:80` | LAN host .203 |
| `opnsense-2@file` | https://opnsense.lab.example | →https | `http://10.0.11.1:80` | LAN host .1 |
| `opnsense@file` | https://router.lab.example | →https | `http://10.0.11.1:80` | LAN host .1 |
| `pikvm-wifi@file` | https://pikvm-wifi.lab.example | →https | `https://10.0.12.36` | LAN host .36 |
| `pikvm@file` | https://pikvm.lab.example | →https | `https://10.0.11.220` | LAN host .220 |
| `prusapi@file` | https://host4.local:80 |  | `http://10.0.11.220:8810` | LAN host .220 |
| `pve-io@file` | https://io.lab.example | →https | `https://10.0.11.246:8006` | LAN host .246 |
| `pve-mini-g5@file` | https://pve-mini-g5.lab.example | →https | `https://10.0.11.190:8006` | LAN host .190 |
| `pve-pluto@file` | https://pluto.lab.example | →https | `https://10.0.11.180:8006` | LAN host .180 |
| `pve-router@file` | https://pve.lab.example | →https | `https://10.0.11.200:8006` | LAN host .200 |
| `pvedocker@file` | https://pvedocker.lab.example | →https | `https://10.0.11.201` | LAN host .201 |
| `go-link@file` | https://go.lab.example | →https | `https://go.lab3.example` | by name: go.lab3.example |
| `zigbee@file` | https://zigbee.lab.example | →https | `http://home2.lan:8080` | by name: home2.lan |

### 4. API exposure and credential hygiene
- **`api.insecure` is off.** There's no `traefik` entrypoint on :8080. The API is published through routers instead: `api@file` and `websecure-api@file` match `(Host(traefik.home1.lan) || Host(traefik.lab.example)) && PathPrefix(/api)` behind the `auth@file` basicAuth middleware. Unauthenticated `GET /api/version` and `/api/rawdata` both return **401**.
- **The credentials go over plain `http://`.** The configured dashboard URL is `http://`, so every poll sends the basic-auth header in clear text across the LAN. This confirms the Homepage README's expected hint, "credentials sent over plain http://", for Traefik.
- **The dashboard UI is unauthenticated on the LAN name.** `traefik-local@file` routes `Host(traefik.home1.lan)` on `web` to `api@internal` with no middleware. API calls on that host still hit `api@file` (priority 74 beats 23, by rule length) and get the auth prompt. It's safe today only because of default priorities: one shorter rule and the API is open.
- **Traefik's API returns the basicAuth htpasswd hashes** (`auth@file` has 2 users) to anyone who can read the API. Cha Dash must not store, log or show middleware configs raw: redact `basicAuth.users`, `digestAuth.users`, forwardAuth/headers values and plugin config.
- `api@file` sets no `entryPoints`, so Traefik attached it to every non-TLS entrypoint, including the TCP ones (`:2222`, `:3128`, `:61000`).

### 5. Surprises
- **Five routers on `web` (:80) carry `tls: {options: default}`**: `homeassistant-local`, the outline LAN name, `paste`, `prusapi`, `plex-rewrite`. They only answer TLS on port 80. Most likely a config slip. That's a good "Traefik lint" hint, and `plex-rewrite`'s `http://` redirectRegex can never fire.
- `plex-rewrite` matches **`Host(app.plex.tv)`**, a public domain served locally through DNS override, and redirects it to the owner's Plex. The sanitizer keeps public domains, so the fixture shows it.
- One service has no router (`cwa@file`), and two middlewares are unused.
- The TCP routers do SPICE console passthrough to the io host (`HostSNI(*)` on `:3128`, TLS passthrough on `:61000`) and SSH passthrough to Onedev on jupiter. A `routes_to` target can be a TCP service, not just HTTP.

## What the integration needs (for `routes_to`, `exposes` and open URLs)

1. **A route resource per router** (`traefik:<instance>/router/<name@provider>`), with the rule parsed into `hosts[]` (`Host`, plus a `HostRegexp` flag), `path` (`PathPrefix`/`Path`), `entryPoints`, `tls`, `priority`, `provider`, `status`. Middlewares are classified as `auth` (basic/forward/digest), `redirect`, `stripPrefix`, `headers` or `other`. **Never store middleware configs raw.**
2. **Open URL:** scheme `https` if the router has `tls`, else `http`. The port is the entrypoint's when it isn't the scheme default. Then the first `Host()`, then the `PathPrefix` unless it's `/`. HostRegexp-only and `HostSNI(*)` routers get no open URL.
3. **Backend resolution, in order:**
   - (a) A bridge IP on the Traefik host → a container on that engine, matched by `NetworkSettings.Networks[*].IPAddress`.
   - (b) An engine host's LAN IP plus a port → a container by `HostConfig.PortBindings` (**including stopped containers**), or by host-network `ExposedPorts`, or by a macvlan (`br0`) container's own IP.
   - (c) Another LAN IP → a PVE guest (agent IP) or a known host.
   - (d) A name → resolve on the hub's DNS.
   - Strip the server URL's path before matching host:port, but keep it for the second hop.
4. **The second hop:** when the backend container is itself a reverse proxy (nginx-proxy, recognized by image), follow it with the target containers' `VIRTUAL_HOST`/`VIRTUAL_PATH`/`VIRTUAL_PORT` env values. That needs **a server-side allowlist of non-secret env keys** that are read for correlation before redaction (see 4.4).
5. **Relations:** `route --routes_to--> app`, plus `route --part_of--> traefik`. When resolved, `container --exposes--> app` at the port, and `app --runs_on--> container`. A route whose container is stopped, or whose port is unbound, gets a hint instead of an edge.
6. **Health:** use the hub's own HTTP check on the routed URL (and on the backend URL when SSO sits in front). Never use `serverStatus` unless the service has `loadBalancer.healthCheck`.
7. **Polling:** the API is cheap (23 KB of routers, 17 KB of services). Poll `/api/http/routers`, `services` and `middlewares` every 60 s, or `/api/rawdata` in one call. Treat 401 as a Repair ("Traefik rejected the credentials").

## Recommendations for v0.1
- Ship the resolution chain above. Of this home's 30 jupiter routes, it resolves 19 to running containers (3 through nginx-proxy) and 2 to the Unraid UI. It flags 4 routes to stopped containers and 5 routes that point at nothing.
- Raise the **plain-http credentials** hint for Traefik. Consider a quiet **"dashboard open without auth on a LAN name"** hint when an `api@internal` router has no auth middleware.
- Add a few Traefik lint hints. They're cheap and come straight from this home: TLS on a plain-HTTP entrypoint, routers to stopped or absent backends, unused services and middlewares.
- Treat the integration base URL and the open URL as **two different fields**. SSO-fronted path routes make them differ for every *arr app here.
