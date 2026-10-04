# Traefik fixture: dogfood home (pvedocker)

The recorded, sanitized responses of the owner's Traefik API (v3.6.7, Docker and File providers), captured read-only on 2026-10-04 by [spike 4.3](../../../spikes/traefik/README.md). It seeds the v0.1 Traefik integration (`proxy.routes`) and the correlator's `routes_to` / open-URL logic.

## Files

| File | API path | What's in it |
|---|---|---|
| `version.json` | `/api/version` | Version, codename, start date |
| `overview.json` | `/api/overview` | Counts (61 HTTP routers · 56 services · 7 middlewares, the same numbers Homepage's widget showed), providers, features |
| `entrypoints.json` | `/api/entrypoints` | `web` :80, `websecure` :443 (TLS), and the TCP entrypoints `onedev-ssh` :2222, `spice` :3128, `spice-tls` :61000. **No `traefik` :8080 entrypoint**, so `api.insecure` is off |
| `http-routers.json` | `/api/http/routers` | 61 routers: 56 `@file`, 5 `@docker`. Includes 7 path-based app routes on `jupiter.lab.example` (`/sonarr`, `/radarr`, `/radarrhd`, `/bazarr`, `/prowlarr`, `/tautulli`, `/jackett`) |
| `http-services.json` | `/api/http/services` | 56 services with `loadBalancer.servers[].url` and `serverStatus` |
| `http-middlewares.json` | `/api/http/middlewares` | `auth@file` (basicAuth), `authelia@docker` (forwardAuth), `redirect-to-https@file`, a headers middleware, a redirectRegex, two **unused** stripPrefix middlewares |
| `tcp-routers.json`, `tcp-services.json` | `/api/tcp/…` | 3 TCP routers: SPICE (plain and TLS passthrough) to the io host, SSH passthrough to jupiter |
| `tcp-middlewares.json`, `udp-*.json` | | Empty lists |
| `capture.json` | | How the capture went, with no identifying values: scheme `http`, no TLS, `401` without credentials on `/api/version` and `/api/rawdata`, `200` with them |

Paginated endpoints were read with `per_page=100`. Everything fit on one page.

## How it was sanitized

The same rules as [the Homepage fixture](../../importers/homepage/dogfood/README.md#how-this-fixture-was-sanitized), applied by `spikes/traefik sanitize`. Everything else is verbatim, including router names, priorities, ordering and quirks.

| Original | Fixture |
|---|---|
| The main personal domain | `lab.example`. The second personal domain (Mealie and Immich in the Homepage fixture) is `family.example`, and a third is `lab3.example` |
| LAN-only names (`*.lan`, mDNS `*.local`) | `home1.lan`, `host1.local`, … The suffix is kept so it's still visible that these can't get a public certificate |
| Escaped domains in `HostRegexp` patterns | Rewritten in escaped form, e.g. ``HostRegexp(`^.+\.code\.lab\.example$`)`` |
| LAN addresses (jupiter's /24) | `10.0.11.x`, last octet kept: jupiter is `10.0.11.120` as in the Homepage fixture |
| Another private /24 (a Wi-Fi VLAN) | `10.0.12.x` |
| Docker bridge addresses (`172.16.0.0/12`) | `10.(100+n).c.d` for `172.(16+n).c.d`, so `/16` subnets and `.1` gateways keep their shape |
| basicAuth `users` (Traefik's API returns the htpasswd hashes) | `admin:` plus a fake hash with the same scheme and bcrypt cost (`$2a$12$…`) |
| Public domains (`app.plex.tv` in the Plex redirect rule) | Kept |

Path rules, ports and entrypoints are unchanged. The fixture contains no real domain, address, user name or hash.

**Leak check:** `spikes/traefik` has `TestFixturesLookSanitized`, which always runs: every address must be in a fake range and every `Host()` must be under a fake or public domain. It also has `TestFixturesHaveNoRealValues`, which scans for every real value seen during capture. That list stays outside the repo, and the test runs with `LEAKCHECK_VALUES=<path>`. Both passed when the fixture was committed.

## What a consumer should get from it

Run `go run . analyze --fixtures ../../testdata/integrations/traefik --docker ../../testdata/integrations/docker/jupiter` in `spikes/traefik` for the full table. The essentials:

- **Router → service:** a router names its service without a provider suffix when both come from the same provider (`"service": "sonarr"` → `sonarr@file`). Qualify it with the router's provider.
- **Backends:** 30 file-provider routers point at jupiter (`10.0.11.120:<port>`), 19 at other LAN hosts, 2 at host names, 5 at `@internal` services. The 5 docker-provider routers point at container IPs on pvedocker's bridge network (`10.106.0.x`).
- **jupiter correlation** (by host port, using the Docker fixture's `PortBindings`):

  | Result | Count | Routers |
  |---|---|---|
  | Running container | 19 | e.g. `bazarr` → `bazarr`, `plex` → `Plex-Current` (host network), `qbittorent` → `binhex-qbittorrentvpn` |
  | Via nginx-proxy | 3 of the 19 | `sonarr`, `radarr` and `radarrhd` go to `10.0.11.120:7780/<path>`, which is the `nginxproxy` container. It routes on to the app by its `VIRTUAL_*` env |
  | Stopped container | 4 | `booklore` → `BookLore` [created], `manga` → `Tachidesk-Docker`, `share` → `pingvin-share`, `romm` → `RomM` [exited] |
  | The Unraid host's UI (:80) | 2 | `jupiter@file`, `jupiter_base@file` (catch-all at priority 998/997 under the path routes at 999) |
  | Nothing binds the port | 5 | `jackett` 9117, `nextcloud` 8688, `paste` 50009, `picsur` 8187, `tubearchivist` 18000 |

- **`serverStatus` is `UP` for every server**, even the 9 with nothing running behind them. No service defines a health check, so `UP` means nothing here and must not drive health.
- **Five routers on `web` (:80) have `tls: {options: default}`**, so they only answer TLS on port 80. That looks like a config slip, which makes it a candidate for a lint hint.
