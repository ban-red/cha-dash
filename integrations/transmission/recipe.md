---
title: The RPC account, which has full control
summary: Transmission has a single RPC username and password and no read-only mode, so whatever signs in can add, remove and change torrents and settings. Cha Dash only reads speeds and counts.
access: full
grants: [Transmission RPC]
discover:
  - {scheme: http, port: 9091, path: /transmission/web/, match: Transmission}
---

## Turn on RPC authentication

Stop Transmission (it rewrites `settings.json` on exit), then set these in `settings.json` and start it again. Transmission replaces the plain password with a hash when it starts.

```json
"rpc-authentication-required": true,
"rpc-username": "chadash",
"rpc-password": "<a long password>"
```

In the container images, the same settings are usually environment variables, `USER` and `PASS`.

## Fill in the form

Put Transmission's address into **URL** (without `/transmission/`), the username into **Username** and the password into **Password**. Change **RPC path** only if you changed `rpc-url`.

## Notes

- `rpc-whitelist` lets in only the addresses it lists. If Test connection says Transmission refused the hub's address, add the hub's address there, or turn `rpc-whitelist-enabled` off and rely on the password.
- Without a password, anything that reaches Transmission has full control of it. Cha Dash says so in Test connection.
- Prefer an `https://` URL through your reverse proxy: the password crosses the network on every request.
