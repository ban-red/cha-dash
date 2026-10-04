---
title: The API key, which has full access
summary: Prowlarr has one API key and it can do everything, including changing indexers and the apps they sync to. There is no read-only key. Cha Dash only reads indexer health and grab counts.
access: full
grants: [Prowlarr API key]
discover:
  - {scheme: http, port: 9696, path: /, match: Prowlarr}
---

## Copy the API key

In Prowlarr, **Settings → General → Security → API Key**. Paste it into **API key**, and put Prowlarr's address, with its URL base if it has one, into **URL**.

## Notes

- Keep Prowlarr off the open internet, or behind your reverse proxy's sign-in: the key is all an attacker would need.
- Regenerating the key cuts off everything that uses it, Cha Dash included.
