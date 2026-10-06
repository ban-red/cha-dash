---
title: The API key, which has full access
summary: Lidarr has one API key and it can do everything, including changing settings and deleting artists. There is no read-only key. Cha Dash only reads the queue, what's wanted and the calendar.
access: full
grants: [Lidarr API key]
discover:
  - {scheme: http, port: 8686, path: /, match: Lidarr}
---

## Copy the API key

In Lidarr, **Settings → General → Security → API Key**. Paste it into **API key**, and put Lidarr's address, with its URL base if it has one, into **URL**.

## Notes

- Cha Dash's one action here, searching for missing albums, uses this same key: Lidarr has no separate key for changes.
- Regenerating the key in Lidarr cuts off everything that uses it, Cha Dash included; Cha Dash then asks to reconnect.
- Keep Lidarr off the open internet, or behind your reverse proxy's sign-in: the key is all an attacker would need.
