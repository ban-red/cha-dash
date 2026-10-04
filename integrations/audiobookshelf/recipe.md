---
title: A user of its own that can't change anything
summary: An Audiobookshelf user of type "user" with no upload, change or delete permission can read its libraries and nothing more. Open listening sessions are shown only to an admin, so with this user they stay unknown; an admin's key reads them too but can change everything.
access: read-only
params:
  - name: user
    label: User name
    default: chadash
    pattern: '^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$'
grants: [Audiobookshelf user]
discover:
  - {scheme: http, port: 13378, path: /status, match: audiobookshelf}
  - {scheme: http, port: 80, path: /status, match: audiobookshelf}
---

## Add the user

In Audiobookshelf, **Settings → Users → Add user**: name it `{{user}}`, account type **User**, and leave **Can download**, **Can update**, **Can delete** and **Can upload** off. Keep **Can access all libraries** on, or tick the libraries Cha Dash should count.

## Make its API key

Audiobookshelf 2.26 and later: **Settings → API Keys → Add API key**, acting as `{{user}}`, with no expiry or one you'll remember to renew. Copy the key; it's shown once. On older releases, open the user under **Settings → Users** and copy its **API token** instead.

Paste it into **API key**.

## Notes

- To see open listening sessions, use an admin's key instead. Test connection then reports full access.
- Books and podcasts are counted in the libraries the user can see.
