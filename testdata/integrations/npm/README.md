# Nginx Proxy Manager API fixture (synthetic)

**Synthetic.** Written by hand in the shape Nginx Proxy Manager 2.12 answers, from its API schema (`backend/schema` in the NPM repository). Hosts are `*.lab.example`, addresses `10.20.30.0/24`; the token and the advanced config's header value are fake.

| File | Endpoint | Notes |
|---|---|---|
| `status.json` | `GET /api/` | Answers without a token: version 2.12.6 |
| `token.json` | `POST /api/tokens` with `{"identity", "secret"}` | A token that lapses a day after the harness clock (2026-10-03 14:05 UTC) |
| `proxy_hosts.json` | `GET /api/nginx/proxy-hosts` | Sonarr with a certificate, forced TLS and a custom location `/api/` to another port; a NAS on two hosts behind an access list, forwarded over https; a disabled host. Booleans as `1`/`0` and `true`/`false`, as NPM's versions differ |
