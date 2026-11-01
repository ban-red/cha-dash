# Caddy admin API fixture (synthetic)

**Synthetic.** Written by hand in the shape Caddy 2.10's admin API answers `GET /config/apps/http/servers` with for a Caddyfile adapted to JSON: each site block a route matching its hosts, its directives a `subroute`. Hosts are `*.lab.example`, addresses `10.20.30.0/24`, and the basic-auth hash is fake.

| File | Endpoint | Notes |
|---|---|---|
| `servers.json` | `GET /config/apps/http/servers` | `srv0` on :443 with four sites: Sonarr proxied by address (after a `headers` handler), `home.lab.example` with `/bazarr*` behind basic auth proxied by container name and the rest a file server, a NAS on two hosts proxied over TLS, and a wildcard whose upstream is a placeholder. `srv1` on :8080 with no host match and two upstreams |
