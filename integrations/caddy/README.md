# Caddy (`caddy`)

Reads Caddy's admin API. Each `reverse_proxy` in the HTTP servers' routes becomes a route resource. Each route has an open URL and its upstream's address, in the shape Traefik's routes have, so the correlator joins it to containers and apps the same way.

**Tier:** bronze (fixture tests). **Capability:** `proxy.routes`. **Secrets:** none: the admin API has no credentials.

## Config

```json
{"url": "http://10.0.0.5:2019"}
```

| Key | Meaning |
|---|---|
| `url` | Where the admin API answers. By default Caddy answers only on `localhost:2019`; the [recipe](recipe.md) moves it to a private address |
| `tls` | The shared TLS policy, for an `https://` URL |

## What it reads

`GET /config/apps/http/servers` every 60 s while watched, every 5 minutes otherwise. Caddy answers `null` when the http app isn't configured, which publishes no routes.

## What it publishes

- **`caddy:<instance>/app`**, kind `app`, `attrs.type = "caddy"`, `servers`, `routes`, `poll_url`. It's monitored by the instance. Caddy has no version endpoint, so there's no version.
- **A route per `reverse_proxy`:** `caddy:<instance>/route/<first host and path>`, kind `route`, `part_of` the app. One that balances several upstreams is still one route. A second route with the same host and path gets `-2`, and so on, never an ID another route has.
  - The match: a route's own `host` and `path` matchers narrow the enclosing routes' (a Caddyfile site block is a route with a `subroute`).
  - `attrs`: `rule` ("host a b, path /x*"), `hosts`, `path`, `entrypoint` (the server's name, `srv0`), `position` (where in the config), `tls`.
  - `url`: https on a server that listens on 443, http on 80, else http on the server's first port. Left out for a wildcard host or no host match.
  - `upstream` and `upstream_addr`: from the first upstream's `dial`, https when the transport has TLS; with more than one, `servers` lists them all, as Traefik's routes do. A placeholder (`{http.request.host}`) or a unix socket is shown and never joined.
- At most 500 routes. Headers, auth handlers (and their password hashes), TLS settings and every other handler are never kept.

## Security finding

Anyone who can reach the admin API can rewrite Caddy's config, and Cha Dash reaching it at all shows it's on the network. Every sync raises `caddy.<instance>.hint.admin_open`, `category: "security"`, severity `attention` (the security posture decides what the Inbox shows). Test connection warns `admin_open` too.

## Repairs

- A 404 means this isn't the admin API, and a 401 or 403 means Caddy's `origins` refused the request. Either is an `api_changed` Repair on `url`.
- An answer that isn't Caddy's config is the same Repair.

## Fixtures

[`testdata/integrations/caddy`](../../testdata/integrations/caddy/README.md): synthetic, a Caddyfile-shaped config with subroutes, basic auth, TLS upstreams, a wildcard and a server without host matches.
