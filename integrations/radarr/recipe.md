---
title: The API key, which has full access
summary: Radarr has one API key and it can do everything, including changing settings and deleting movies. There is no read-only key. Cha Dash only reads the queue, what's wanted and the calendar.
access: full
grants: [Radarr API key]
discover:
  - {scheme: http, port: 7878, path: /, match: Radarr}
---

## Copy the API key

In Radarr, **Settings → General → Security → API Key**. Paste it into **API key**, and put Radarr's address, with its URL base if it has one, into **URL**.

## Notes

- Regenerating the key in Radarr cuts off everything that uses it, Cha Dash included; Cha Dash then asks to reconnect.
- Keep Radarr off the open internet, or behind your reverse proxy's sign-in: the key is all an attacker would need.
