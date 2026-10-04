---
title: The API key, which has full access
summary: Bazarr has one API key and it can do everything, including changing providers and settings. There is no read-only key. Cha Dash only reads what's still wanted and Bazarr's health.
access: full
discover:
  - {scheme: http, port: 6767, path: /, match: Bazarr}
---

## Copy the API key

In Bazarr, **Settings → General → Security → API Key**. Paste it into **API key**, and put Bazarr's address, with its URL base if it has one, into **URL**.

## Notes

- Keep Bazarr off the open internet, or behind your reverse proxy's sign-in: the key is all an attacker would need.
