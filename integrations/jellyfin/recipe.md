---
title: An API key, which acts as an administrator
summary: Jellyfin's API keys act as an administrator, and there are no read-only ones. A key of its own lets you revoke Cha Dash alone. Cha Dash only reads what's playing and the library counts.
access: full
grants: [Jellyfin API key]
discover:
  - {scheme: http, port: 8096, path: /System/Info/Public, match: Jellyfin}
---

## Create the key

In Jellyfin, **Dashboard → API Keys → +**, named Cha Dash. Paste the key into **API key**, and put Jellyfin's address, with its base URL if it has one, into **URL**.

## Notes

- Delete the key on the same page to cut Cha Dash off; it then asks to reconnect.
- Keep Jellyfin's API keys private: each one can change the server's settings and users.
