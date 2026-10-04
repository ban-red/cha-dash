---
title: The API key, which has full access
summary: Seerr (Overseerr or Jellyseerr) has one API key, and it acts as an administrator. There is no read-only key. Cha Dash only reads the request counts and the version.
access: full
discover:
  - {scheme: http, port: 5055, path: /api/v1/status, match: commitTag}
---

## Copy the API key

In Seerr, **Settings → General → API Key**. Paste it into **API key**. Overseerr and Jellyseerr have it in the same place, and connect as Seerr: they share its API.

## Notes

- Keep the key private: it can approve requests, change users and change settings.
