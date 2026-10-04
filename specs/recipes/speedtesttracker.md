---
title: An API token that can only read results
summary: Speedtest Tracker's API tokens carry abilities. One with only "read results" can't start tests or change anything. Cha Dash only reads the latest result.
access: read-only
discover:
  - {scheme: http, port: 80, path: /, match: Speedtest Tracker}
  - {scheme: http, port: 8080, path: /, match: Speedtest Tracker}
---

## Make the token

In Speedtest Tracker, open **API Tokens** in the admin panel and create a token named `chadash`. Tick only the **results:read** ability (reading results), and pick an expiry you'll remember to renew, or none. Copy the token; it's shown once.

Paste it into **API token**.

## Notes

- Cha Dash needs the v1 API, which arrived with API tokens. On an older Speedtest Tracker, Test connection says the API is missing: update first.
- Until the first test finishes, the tile has nothing to show and says so.
