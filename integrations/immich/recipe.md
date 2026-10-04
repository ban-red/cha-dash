---
title: An API key with four read permissions
summary: An Immich API key limited to the server's version and statistics and the user's own library numbers. It can't upload, change or delete anything. Make it as an administrator to count the whole server.
access: read-only
grants: [asset.statistics, server.about, server.statistics, user.read]
discover:
  - {scheme: http, port: 2283, path: /api/server/ping, match: pong}
---

## Create the key

1. Signed in to Immich as an administrator, open **Account Settings → API Keys → New API Key**, named Cha Dash.
2. Choose its permissions instead of **all**: `server.about`, `server.statistics`, `asset.statistics` and `user.read`.
3. Immich shows the key once: paste it into **API key**.

## Notes

- With a key that isn't an administrator's, Cha Dash counts only that user's photos and videos, and says so.
- A key with **all** works too, and Test connection flags it as able to change things. Immich releases without per-key permissions only make keys like that.
