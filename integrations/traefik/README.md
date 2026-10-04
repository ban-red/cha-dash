# Traefik (`traefik`)

Reads Traefik's API (v2 and v3). It publishes the Traefik app, a resource per HTTP service, and a resource per HTTP router. Each route gets its open URL and its backend URL, resolved from Traefik's own services rather than from container labels. Middleware configs are never kept.

**Tier:** silver (repairs, version detection, fixture tests). **Capability:** `proxy.routes`. **Secrets:** `password` (optional).

Built from [spike 4.3](../../spikes/traefik/README.md) and tested against its capture of the dogfood home's Traefik v3.6.7 ([`testdata/integrations/traefik`](../../testdata/integrations/traefik/README.md)). That's 61 HTTP routers, 56 services, 7 middlewares, and basic auth on the API.

## Config

```json
{"url": "http://traefik.lab.example", "username": "admin", "tls": {"mode": "verify"}, "password": "${secret:instance/<id>/password}"}
{"url": "http://traefik:8080"}
```

| Key | Meaning |
|---|---|
| `url` | Where the API answers. The integration adds `/api/…`; a trailing `/dashboard/` (a pasted dashboard URL) is dropped. `http://` or `https://`. |
| `tls` | The shared TLS policy, for an `https://` URL. |
| `username` | Plain config: identifiers aren't secrets. Required with a password. |
| `password` | The vault secret for basic auth. Required with a username. |

The first line is the shape the Homepage importer writes.

## What it reads

All `GET`, with basic auth when configured. Redirects stay within the origin, so the credentials never leave it.

| Request | Why |
|---|---|
| `/api/version` | the version, every sync, and Test |
| `/api/overview` | the counts the Traefik widget shows, the providers, and error totals |
| `/api/entrypoints` | entrypoint ports and their default TLS, for open URLs |
| `/api/http/routers`, `/api/http/services`, `/api/http/middlewares` | paginated (`per_page=100`, following `X-Next-Page`, at most 50 pages) |
| `/api/version` **without** credentials | every 10 minutes when a username is set: does the API answer anyone? |

**Middleware configs are scrubbed by the decoder.** Only a middleware's name, provider, type, a strip's prefixes and a chain's members are decoded. `basicAuth`/`digestAuth` users (Traefik returns the password hashes), forwardAuth addresses and headers, header values and plugin configs never leave the response. Nothing about them is stored, logged or shown. Router TLS options (cert resolvers, domains) are reduced to "has TLS".

## What it publishes

IDs follow the wave 5 convention. Router and service names keep their provider (`sonarr@file`), since two providers can use one name.

**The app,** `traefik:<inst>/app`, kind `app`, `attrs.type: "traefik"`, named after the instance. It's the instance's service resource. Caps `["proxy.routes"]`.

| Field | |
|---|---|
| `routers`, `services`, `middlewares` | HTTP totals from the overview, the attrs the Traefik widget reads |
| `tcp_routers`, `udp_routers` | when there are any |
| `version`, `providers` | |
| `poll_url` | the API base the integration reads |
| `links` | the dashboard: the routed URL of a router to `api@internal` or `dashboard@internal`, preferring TLS, then no path, then priority (`https://traefik.lab.example` on the capture). Falls back to `<url>/dashboard/` |
| health | `ok`; `attention` when the overview counts HTTP errors |

**Routes,** `traefik:<inst>/route/<router name>`, kind `route`, named without the provider (`sonarr`). One per HTTP router. State is the router's status (`enabled`, `warning`, `disabled`).

| Attr | |
|---|---|
| `rule`, `entrypoint`, `tls`, `upstream` | as in the Demo Home. `entrypoint` is the first one (`entrypoints` lists them all when there are several). `tls` is the router's own TLS or its entrypoint's default |
| `upstream` | **resolved from Traefik's service**: the router's service, qualified with the router's provider when it has none (`"sonarr"` from a file router is `sonarr@file`). Then its load balancer's first server URL, following weighted, mirroring and failover services. Internal services stay as their name (`api@internal`, `noop@internal`) |
| `upstream_addr` | the server's `host:port` (the scheme's port when it has none): what the correlator matches against published ports |
| `upstream_path` | the server URL's path, when it has one. On the dogfood home, `/sonarr`, `/radarr` and `/radarrhd` go to `http://10.0.11.120:7780/<path>`, the **nginx-proxy hop**: `upstream_addr` is the nginx-proxy container's published port, and the correlator follows the path from there |
| `url` | the **open URL** (spike 4.3): `https` when the router has TLS, the entrypoint's port when it isn't the scheme's default, the first `Host()`, then the `PathPrefix`/`Path` unless it's `/`. Also the route's link. Routers matched only by `HostRegexp` have none |
| `poll_url` | the **backend URL** for an API client, past any auth middleware: the server's origin, plus the path the backend sees. That's the route's prefix, unless a `stripPrefix`/`stripPrefixRegex` on the router removes it, preceded by the server URL's path only with `preservePath` (Traefik otherwise ignores it). So `/sonarr` behind Authelia opens at `https://jupiter.lab.example/sonarr` and polls at `http://10.0.11.120:7780/sonarr`. `/bazarr` polls at `http://10.0.11.120:6767/bazarr` |
| `auth` | `basic`, `digest` or `forward` when an auth middleware (chains flattened) is in front |
| `middlewares` | the names, qualified; never their configs |
| `router`, `provider`, `priority`, `service`, `hosts`, `path`, `host_regexp` | natural keys for the correlator |

Route health: `ok` for an enabled router. `attention` for `warning`, `down` for `disabled`, each with Traefik's first error. `attention` ("backend: …") when its service fails Traefik's health check.

**Services,** `traefik:<inst>/service/<service name>`, kind `service`. Attrs: `provider`, `type` (`loadbalancer`, …, or `internal`), `servers`, `upstream`, `used_by`. **`serverStatus` is ignored unless the service has a health check:** without one Traefik reports `UP` for every server, including the 9 on the capture with nothing behind them. With one, `health_check: true` and `server_status` are published, and health is `down` when every server fails and `attention` when some do.

**Relations:** `route part_of app`, `route routes_to service`, `service part_of app`, `integration:<inst> monitors app`. The `routes_to` edges to the apps and containers behind a service are the correlator's (wave 6). It matches `upstream_addr` and `ips` from Docker, `hosts` and `url` from the importer's hrefs.

**Metrics:** none. The counts are attrs, as in the Demo Home. Latency comes from the built-in checks on the routed URLs.

## Poll plan

Every 60 s while watched, every 5 min idle, with a 30 s timeout. The API is cheap (about 80 KB on the dogfood home), so every sync reads everything and publishes one scope. No push.

## Grants, repairs and hints

**Test** reads `/api/version` with the credentials and reports:
- the version;
- one grant, `api` (read), since Traefik's API can't change anything;
- a warning, `api_no_auth`, when the API also answers without credentials.

The runtime adds `plain_http` when the credentials go over `http://`.

**Repairs:**
- 401 or 403 is an auth repair: field `password`, or `username` when none is configured ("Traefik's API asks for credentials").
- 404 on an API path is `api_changed`, field `url`: the API isn't served at that URL.

**Hints** (quiet Inbox problems on the app; they resolve themselves):

| ID | Severity | When |
|---|---|---|
| `hint.plain_http_credentials` | attention | a username is set and the URL is `http://` ("Traefik sends its credentials over plain http:// to …", the importer's wording). The dogfood home earns it |
| `hint.api_no_auth` | attention | the API answers without credentials: anyone who can reach it can read every router, service and middleware, basic-auth hashes included |
| `hint.api_no_auth` | info | otherwise, routers serve the dashboard (`api@internal`) with no auth middleware. On the dogfood home that's `traefik.home1.lan` and `traefik.lab.example`; its API calls still hit the authenticated `/api` routers, but only because their rules are longer |

## Limits
- HTTP routers only. TCP and UDP routers are counted on the app, not published (SPICE and SSH passthrough on the capture).
- The second hop isn't followed here. Telling which container nginx-proxy serves `/radarrhd` from takes the containers' `VIRTUAL_*` environment values, which the Docker integration doesn't keep.
- Lint hints from the spike aren't raised yet: TLS on a plain-HTTP entrypoint, routes to stopped or absent backends, unused services and middlewares. The last two need Docker's view as well.
- `serverStatus` drives health only for services with a health check.
