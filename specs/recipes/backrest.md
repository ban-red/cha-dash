---
title: A Backrest user's password, as a basic auth header
summary: Backrest's API takes HTTP basic auth on every call, and it has no read-only users, so this credential could change Backrest's config. Cha Dash only reads the plans, the dashboard summary and the recent operations.
access: full
---

## Add a user for Cha Dash

In Backrest, open **Settings → Users** and add a user, say `chadash`, with a long random password. Backrest's users can all do everything, so a user of its own is only there to keep this password apart from yours.

## Encode it

Basic auth sends `user:password` in base64, and a spec can't encode it, so encode it once yourself:

```sh
printf '%s' 'chadash:the-password' | base64
```

Paste the output into **Basic auth**.

## Say what it backs up

Set **Host it backs up** to the address of the machine whose folders Backrest reads (the URL's host is used otherwise). When Backrest runs in a container, set **Path map** to its folder and the host's, such as `/userdata:/mnt/user`, so a plan's `/userdata/appdata` counts as the host's `/mnt/user/appdata` on the coverage map.

## Notes

- Written from Backrest v1.14.1's source (its Connect API in `proto/v1/service.proto` and `internal/api`), not checked against a running Backrest yet.
- Each plan is a backup target: its paths are what it covers, its newest finished backup is the last run, and Backrest's own overdue flag marks it stale.
- A failed backup's message comes from the last 100 operations; an older failure shows without one.
- Signing in for a token (Backrest's Login, a JWT for 7 days) is a handshake a spec can't do, so the password goes in the header itself.
