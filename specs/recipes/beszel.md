---
title: A read-only user's token, made with Impersonate
summary: Beszel has a read-only role, and its hub is a PocketBase app whose superusers can make a long-lived token for any user. A read-only user's token lists the systems shared with it and can't change anything. Cha Dash only reads.
access: read-only
params:
  - name: user
    label: User's email
    default: chadash@home.example
    pattern: '^[A-Za-z0-9._%+-]{1,64}@[A-Za-z0-9.-]{1,190}$'
discover:
  - {scheme: http, port: 8090, path: /api/health, match: healthy}
---

## Add a read-only user

In Beszel, open **Settings → Users** (or PocketBase's admin at `/_/`, collection **users**) and add a user `{{user}}` with the role **readonly**. Give it any password: Cha Dash never uses it.

## Share the systems with it

Beszel shows a user only the systems shared with it. For each system Cha Dash should show, edit it and add `{{user}}` under **Users**. A system that isn't shared isn't listed.

## Make its token

Open PocketBase's admin at `/_/` as a superuser, go to the **users** collection, open `{{user}}` and choose **Impersonate** from its menu. Set a long **token duration** (a year is 31536000 seconds), generate the token and paste it into **Auth token**.

## Notes

- Signing in with the user's password needs a handshake (PocketBase's `auth-with-password`, then the token it returns), which a spec can't do. That's why this takes a token made ahead of time.
- An impersonated token can't be refreshed. When it expires, Cha Dash shows a repair on **Auth token**: make a new one the same way.
- Changing `{{user}}`'s password cuts the token off, as PocketBase then signs that user's tokens with a new key.
- CPU, memory and disk are the latest readings, on each system as `cpu_pct`, `mem_pct` and `disk_pct`. Beszel keeps their history itself.
