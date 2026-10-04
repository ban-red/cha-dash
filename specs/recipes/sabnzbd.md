---
title: The API key, which has full access
summary: SABnzbd's API key can do everything, including changing settings and deleting downloads. Its other key, the NZB key, can only add downloads and can't read the queue, so it won't do. Cha Dash only reads the queue's totals.
access: full
discover:
  - {scheme: http, port: 8080, path: /sabnzbd/, match: SABnzbd}
---

## Copy the API key

In SABnzbd, **Config → General → Security → API Key** (not the NZB key). Paste it into **API key**, and put SABnzbd's address, with its URL base (`/sabnzbd` by default), into **URL**.

## Notes

- SABnzbd takes the key in the request's address, so it can turn up in a reverse proxy's access log. Cha Dash keeps it out of its own errors and logs.
- Regenerating the key cuts off everything that uses it, Cha Dash included.
