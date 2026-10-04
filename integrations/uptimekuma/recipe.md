---
title: A status page, and an optional metrics key
summary: Cha Dash reads one public status page, which needs no credential. An API key adds live status and response times from /metrics; it can't sign in or change anything.
access: read-only
grants: [metrics]
discover:
  - {scheme: http, port: 3001, path: /, match: Uptime Kuma}
---

## Pick the status page

The monitors Cha Dash shows are the ones on a status page. Use one you already have, or make one under **Status Pages → New Status Page** with the monitors you want on your page. Its slug is the end of its address, `/status/<slug>`: put it in **Slug**.

## Optionally, make a metrics key

**Settings → API Keys → Add API Key**, named Cha Dash. Paste it into **Key**. Without one, status comes from the status page, which can be a minute behind.

## Notes

- A status page is public: anyone who can reach Uptime Kuma can read it, with or without Cha Dash.
- An Uptime Kuma API key only opens `/metrics`, for every monitor. Cha Dash still shows only the status page's monitors.
