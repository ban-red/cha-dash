---
title: A scoped API key that can only list projects
summary: Arcane's scoped API keys carry their own permissions. A key with only projects:list can read the compose projects and their state, and can't deploy, stop, edit or open a shell in anything. Cha Dash only reads.
access: read-only
params:
  - name: name
    label: Key name
    default: chadash
    pattern: '^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$'
discover:
  - {scheme: http, port: 3552, path: /api/app-version, match: svelteKitVersion}
  - {scheme: https, port: 3552, path: /api/app-version, match: svelteKitVersion}
---

## Make a scoped API key

Signed in to Arcane as an admin, open **Settings → API Keys** and create a key named `{{name}}`. Keep it **scoped** (not a personal key, which would carry your own role's permissions).

## Give it one permission

In the key's permissions, tick **projects:list** and nothing else. If the key can be limited to an environment, pick the one Cha Dash should read.

projects:list reads each project's name, state, services and image update status. It doesn't show compose files or `.env` values, and every action (deploy, down, restart, update, edit) needs another permission the key doesn't have.

## Paste it into Cha Dash

Arcane shows the key once, starting `arc_`: paste it into **API key**. Leave **Environment ID** at `0` for the Docker host Arcane runs on; for a remote environment, use the ID from its address in Arcane.

## Notes

- Each project becomes a stack in Cha Dash, the first 50 by name. Archived projects are left out.
- Deleting the key in Arcane cuts Cha Dash off at once.
