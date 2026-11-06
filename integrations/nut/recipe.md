---
title: No credential, or a user that can't change anything
summary: upsd lets any host that can connect read every UPS, so Cha Dash needs no credential. If your upsd asks for one, give it a user in upsd.users with no actions, no instcmds and no upsmon role. That user can't change anything.
access: read-only
params:
  - name: user
    label: User name
    default: chadash
    pattern: '^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$'
---

## Let the hub connect

In `upsd.conf` on the machine running upsd, upsd must listen on an address the hub can reach. By default it listens only on localhost:

```text
LISTEN 0.0.0.0 3493
```

Restart upsd after the change. Then fill in **Host** and leave **Port** at 3493.

## Only if upsd asks for a user

Most setups need nothing here: leave **Username** and **Password** empty. If your upsd refuses reads (a firewall rule or a wrapper in front of it), add a user to `upsd.users` with a long password of its own and nothing else:

```text
[{{user}}]
    password = <a long random password>
```

No `actions`, `instcmds` or `upsmon` line: those let a user change settings, run commands on the UPS or shut systems down. Put `{{user}}` into **Username** and the password into **Password**.

## Notes

- NUT's protocol is plain TCP: a password crosses the network in clear. That's why the recipe prefers no credential at all.
- Cha Dash never sends `LOGIN`, so it doesn't count as a upsmon client and never delays a shutdown.
- Restrict port 3493 to your LAN with a firewall rule: anyone who can reach it can read the UPS.
