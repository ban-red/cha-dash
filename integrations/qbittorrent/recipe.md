---
title: The WebUI account, which has full control
summary: qBittorrent has a single WebUI account and no read-only mode, so whatever signs in can add, remove and change torrents and settings. Cha Dash only reads speeds and counts.
access: full
grants: [qBittorrent WebUI]
discover:
  - {scheme: http, port: 8080, path: /, match: qBittorrent}
---

## Use the WebUI account

Put the WebUI's username into **Username** and its password into **Password** (**Tools → Options → Web UI → Authentication**). The password goes into Cha Dash's encrypted vault.

## Notes

- Pausing and resuming all torrents from Cha Dash uses this same account: qBittorrent has no separate account for changes.
- **Bypass authentication for clients in whitelisted IP subnets** lets Cha Dash in without a password, but it also gives anything at that address full control without one. Cha Dash flags it.
- qBittorrent bans an address after a few failed sign-ins. If Test connection says the hub is banned, wait, or restart qBittorrent.
