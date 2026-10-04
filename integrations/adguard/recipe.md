---
title: A user of its own, which is an administrator
summary: Every AdGuard Home user is an administrator; there are no read-only users. A user just for Cha Dash still helps, because you can remove it without touching yours. Cha Dash only reads the statistics and the filter status.
access: full
params:
  - name: user
    label: User name
    default: chadash
    pattern: '^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$'
fills:
  username: '{{user}}'
grants: [AdGuard Home user]
discover:
  - {scheme: http, port: 80, path: /login.html, match: AdGuard Home}
  - {scheme: http, port: 3000, path: /login.html, match: AdGuard Home}
---

## Make a password hash

On any machine with `htpasswd` (Apache's tools). It asks for the password, so it never lands in your shell history:

```sh
htpasswd -nB {{user}}
```

## Add the user to AdGuardHome.yaml

Stop AdGuard Home, add an entry under `users:` with the name and the hash after the colon, then start it again.

```yaml
users:
  - name: {{user}}
    password: <the hash htpasswd printed after "{{user}}:">
```

## Fill in the form

Put `{{user}}` into **Username** and the password you typed into **Password**.

## Notes

- AdGuard Home without any users lets anyone who reaches it change everything. Cha Dash flags that too.
- Prefer an `https://` URL: the password crosses the network on every request.
