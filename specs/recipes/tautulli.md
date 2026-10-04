---
title: The API key, which has full access
summary: Tautulli has one API key and it can do everything its API offers, including changing settings and deleting history. There is no read-only key. Cha Dash only reads what's playing and the server's status.
access: full
discover:
  - {scheme: http, port: 8181, path: /, match: Tautulli}
---

## Copy the API key

In Tautulli, **Settings → Web Interface → API**: turn on **Enable API**, then copy the **API key** into **API key** here.

## Notes

- Tautulli takes the key in the request's address, so it can turn up in a reverse proxy's access log. Cha Dash keeps it out of its own errors and logs.
- Regenerating the key in Tautulli cuts off everything that uses it, Cha Dash included.
