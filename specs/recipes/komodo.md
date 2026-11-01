---
title: A service user that can only read stacks and servers
summary: Komodo's API keys act as their user, with that user's permissions. A service user of its own, given Read on stacks and servers and nothing else, can list them and can't deploy, stop or change anything. Cha Dash only reads.
access: read-only
params:
  - name: user
    label: Service user name
    default: chadash
    pattern: '^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$'
discover:
  - {scheme: https, port: 9120, path: /, match: Komodo}
  - {scheme: http, port: 9120, path: /, match: Komodo}
---

## Make a service user

Signed in to Komodo as an admin, open **Settings → Users** and add a **service user** named `{{user}}`. Leave it a plain user: not an admin, and without the global permissions to create servers, builds or other resources.

## Let it read stacks and servers

Give `{{user}}` **Read** on stacks and servers, and nothing more:
- either open **Settings → Users → {{user}}** and set **Read** for all **Stacks** and all **Servers**;
- or put it in a user group with those permissions, if you manage access through groups.

Read lists a stack's state and services. It doesn't show its environment or let anyone deploy, stop or edit it: those need Execute or Write, which `{{user}}` doesn't get.

## Make its API key

On the same page, create an **API key** for `{{user}}`. Komodo shows the key and the secret once: paste them into **API key** and **API secret**.

## Notes

- Without Read on servers, stacks still read, without their server's name.
- Komodo counts only what `{{user}}` may read: a stack it can't see isn't listed.
- Deleting the API key in Komodo cuts Cha Dash off at once.
