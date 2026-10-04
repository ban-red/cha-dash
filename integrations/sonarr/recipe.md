---
title: The API key, which has full access
summary: Sonarr has one API key and it can do everything, including changing settings and deleting series. There is no read-only key. Cha Dash only reads the queue, what's wanted and the calendar.
access: full
grants: [Sonarr API key]
discover:
  - {scheme: http, port: 8989, path: /, match: Sonarr}
---

## Copy the API key

In Sonarr, **Settings → General → Security → API Key**. Paste it into **API key**, and put Sonarr's address, with its URL base if it has one, into **URL**.

## Notes

- Regenerating the key in Sonarr cuts off everything that uses it, Cha Dash included; Cha Dash then asks to reconnect.
- Keep Sonarr off the open internet, or behind your reverse proxy's sign-in: the key is all an attacker would need.
