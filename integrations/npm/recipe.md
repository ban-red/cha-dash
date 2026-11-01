---
title: A user that can only view proxy hosts
summary: Nginx Proxy Manager has no API keys, so Cha Dash signs in as a user. Make it a user of its own whose only permission is to view proxy hosts, so it can't change them or see anything else.
access: read-only
params:
  - name: email
    label: Email
    default: chadash@example.com
    pattern: '^[A-Za-z0-9._+-]{1,64}@[A-Za-z0-9.-]{1,190}$'
fills:
  email: '{{email}}'
grants: [view proxy hosts]
discover:
  - {scheme: http, port: 81, path: /api/, match: '"status":"OK"'}
---

## Add the user

In Nginx Proxy Manager, open **Users** and **Add User**. Name it Cha Dash, use `{{email}}` as its email, and leave **Administrator** off.

## Set its permissions

Open the user's **Permissions**. Set **Item Visibility** to *All Items*, **Proxy Hosts** to *View Only*, and everything else to *Hidden*.

## Give it a password

From the user's menu, **Change Password**, and set a long random one.

## Fill in the form

Use the admin page's address as the URL (port 81 by default), `{{email}}` as **Email**, and the password as **Password**.

## Notes

- Cha Dash signs in for a token that lasts a day, and signs in again before it lapses. The password stays in the vault.
- Prefer an `https://` URL: over plain `http://` the password crosses the network at every sign-in.
- Cha Dash keeps each proxy host's domains, forward address and whether it has TLS or an access list, never its advanced nginx config.
