---
title: The admin API on a private address
summary: Caddy's admin API has no credentials of its own, and it can change everything, so the safety is in where it listens. Make it answer on a private address that Cha Dash can reach, and nowhere else.
access: none
discover:
  - {scheme: http, port: 2019, path: /config/apps/http/servers, match: listen}
---

## Make it listen where Cha Dash can reach it

By default the admin API answers only on `localhost:2019`. In the Caddyfile's global options, give it the private address of the host Caddy runs on:

```
{
	admin 10.0.0.5:2019
}
```

In Docker, publish the port on that address only (`10.0.0.5:2019:2019`), never on `0.0.0.0`. When Cha Dash and Caddy share a Docker network, `admin 0.0.0.0:2019` without publishing the port keeps it inside that network.

## Fill in the form

Use `http://10.0.0.5:2019` (your address) as the URL.

## Notes

- Anyone who can reach the admin API can replace Caddy's config. Cha Dash raises a security finding while it reads one, as a reminder to keep it off any network you don't trust.
- Cha Dash only sends `GET /config/apps/http/servers`. It keeps each route's hosts, path and upstream address, never headers, auth settings or TLS keys.
