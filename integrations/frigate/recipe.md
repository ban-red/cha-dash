---
title: A viewer account on the authenticated port
summary: A Frigate user with the viewer role, signing in on port 8971. It sees cameras and events, and can't change the config, restart Frigate or delete recordings.
access: read-only
params:
  - name: user
    label: User name
    default: chadash
    pattern: '^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$'
fills:
  username: '{{user}}'
grants: [Frigate viewer]
discover:
  - {scheme: https, port: 8971, path: /, match: Frigate}
  - {scheme: http, port: 5000, path: /, match: Frigate}
---

## Create the user

In Frigate, **Settings → Users → Add User**. Name it `{{user}}`, give it a long random password, and pick the **viewer** role. To show only some cameras, a custom role limited to them works too.

## Point Cha Dash at port 8971

Set the URL to Frigate's authenticated port, `https://<frigate host>:8971`, with TLS mode `tofu` when Frigate uses its own certificate. Then fill in **Username** and **Password**.

## Notes

- Port 5000 answers without signing in, as an anonymous admin that can change the config and delete recordings. It works, but the Inbox keeps a quiet hint until the instance moves to 8971.
- A Frigate without roles makes every user an admin. Test connection shows which role the account got.
