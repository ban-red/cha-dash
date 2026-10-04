---
title: The restricted user
summary: NZBGet's restricted user can't see or change settings, but it can still pause, resume and delete downloads; there is no read-only user. It's the least NZBGet offers that can read the queue. Cha Dash only reads the speed and the queue's totals.
access: scoped
params:
  - name: user
    label: User name
    default: chadash
    pattern: '^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$'
fills:
  username: '{{user}}'
grants: [NZBGet user]
discover:
  - {scheme: http, port: 6789, path: /, match: NZBGet}
---

## Set the restricted user

In NZBGet, **Settings → Security**: set **RestrictedUsername** to `{{user}}` and **RestrictedPassword** to a long password of its own. Then **Save all changes** and **Reload**.

## Fill in the form

Put `{{user}}` into **Username** and that password into **Password**.

## Notes

- Not the **AddUsername**: that user can only add downloads and can't read the queue.
- The control user (**ControlUsername**) works too, but it can also change every setting.
- Prefer an `https://` URL through your reverse proxy: the password crosses the network on every request.
