# Nginx Proxy Manager (`npm`)

Reads Nginx Proxy Manager's API. Each proxy host, and each of its custom locations, becomes a route resource. Each route has an open URL and its forward address, in the shape Traefik's routes have, so the correlator joins it to containers and apps the same way.

**Tier:** bronze (fixture tests). **Capability:** `proxy.routes`. **Secrets:** `password`.

## Config

```json
{"url": "http://npm.lab.example:81", "email": "chadash@example.com", "password": "${secret:instance/<id>/password}"}
```

| Key | Meaning |
|---|---|
| `url` | The admin page's address (port 81 by default) |
| `tls` | The shared TLS policy, for an `https://` URL |
| `email` | A user's sign-in email. Plain config: it's an identifier |
| `password` | That user's password, in the vault |

NPM has no API keys. The [recipe](recipe.md) makes a user whose only permission is to view proxy hosts.

## What it reads

- `GET /api/` (no token) for the version.
- `POST /api/tokens` with the email and password for a token. It lasts a day, and is renewed an hour before it lapses, or once when the API refuses it. The token lives in memory only.
- `GET /api/nginx/proxy-hosts` with the token.

Every 60 s while watched, every 5 minutes otherwise.

## What it publishes

- **`npm:<instance>/app`**, kind `app`, `attrs.type = "npm"`, `version`, `proxy_hosts`, a link to the admin page. It's monitored by the instance.
- **`npm:<instance>/route/<id>`** per proxy host, kind `route`, `part_of` the app:
  - state `enabled` or `disabled`;
  - `attrs`: `rule`, `hosts`, `proxy_host`, `tls` (a certificate or forced TLS), `auth: "access list"` when there's one, and `entrypoint: "proxy host"`;
  - `url`: https with TLS, else http, on the first domain; left out for a wildcard;
  - `upstream` and `upstream_addr`: from the forward scheme, host and port.
- **`npm:<instance>/route/<id>-<n>`** per custom location, with its own `path` and forward address.
- Advanced nginx config, access lists' contents and certificates are never kept.

## Test connection

Test connection checks the version, a sign-in and the proxy hosts. It warns `plain_http` when the URL is `http://`: the password then crosses the network at every sign-in.

## Repairs

- A refused email or password (400, 401 or 403 on `POST /api/tokens`) is an auth Repair on `password`.
- A 403 on proxy hosts is an auth Repair on `email`: the user can't view them.
- A 404 is an `api_changed` Repair on `url`.

## Fixtures

[`testdata/integrations/npm`](../../testdata/integrations/npm/README.md): synthetic, NPM 2.12.
