---
title: A dashboard login of its own
summary: Traefik's API can only read, so the question is who may reach it. Give Cha Dash its own basic-auth user on the API, rather than reusing yours, and leave the API closed to everyone else.
access: read-only
params:
  - name: user
    label: User name
    default: chadash
    pattern: '^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$'
fills:
  username: '{{user}}'
grants: [api]
discover:
  - {scheme: http, port: 8080, path: /api/version, match: Codename}
---

## Make a password hash

On any machine with `htpasswd` (Apache's tools). It asks for the password, so it never lands in your shell history:

```sh
htpasswd -nB {{user}}
```

## Add it to the API's basic auth

Add the line it prints to the users of the `basicAuth` middleware in front of Traefik's API and dashboard router. In Docker labels, double every `$` in the hash (`$$`).

## Fill in the form

Use the API's address as the URL, `{{user}}` as **Username**, and the password you typed as **Password**.

## Notes

- Test connection warns when the API also answers without credentials (`api.insecure`, or a router without auth).
- Traefik's API hands out every middleware's config, including other basic-auth users' password hashes. Cha Dash drops them as it reads, and never stores or shows them.
- Prefer an `https://` URL: over plain `http://` the password crosses the network on every request.
