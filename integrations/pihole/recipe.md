---
title: An app password, which signs in as the administrator
summary: Pi-hole has a single administrator, and an app password signs in as it; there is nothing read-only. An app password still beats the web password, because it works with two-factor sign-in and can be revoked on its own. Cha Dash only reads the statistics.
access: full
grants: [Pi-hole admin]
discover:
  - {scheme: http, port: 80, path: /admin/, match: Pi-hole}
---

## Create an app password

In Pi-hole v6, **Settings → Web interface / API**, switch to **Expert** mode, then **Configure app password**. Pi-hole shows it once: paste it into **Password**.

## Point Cha Dash at the Pi-hole

Use the Pi-hole's address as the URL. Pi-hole serves HTTPS itself with its own certificate, so `https://` with TLS mode `tofu` fits.

## Notes

- A new app password replaces the old one, which cuts off everything that used it.
- Pi-hole v5 isn't supported: its API takes a different credential, and v5 is out of support.
