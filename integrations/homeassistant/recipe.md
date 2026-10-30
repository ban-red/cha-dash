---
title: A token of a user who isn't an administrator
summary: Home Assistant has no read-only tokens. A long-lived token carries all of its user's rights, so make it for a user who isn't an administrator. That token can still control devices, but can't change the configuration or manage users.
access: scoped
grants: [Home Assistant user]
discover:
  - {scheme: http, port: 8123, path: /manifest.json, match: Home Assistant}
---

## Add a person who can sign in

1. **Settings → People → Add person**, named Cha Dash.
2. Turn on **Allow login**, set a long random password, and leave **Administrator** off.

## Make the token as that person

1. Sign in to Home Assistant as Cha Dash, in a private window.
2. Open the profile, then **Security → Long-lived access tokens → Create token**, named Cha Dash.
3. Home Assistant shows the token once: paste it into **Token**.

## Notes

- To run scripts from Cha Dash, list them under **Scripts Cha Dash may run**. It runs only those, with this same token: Home Assistant's tokens have no scopes, so the list is the limit.
- To switch lights and switches, play and pause media players, or set thermostats from the tile's panel, tick them under **Devices Cha Dash may control**. Same token, same reasoning: the list is the limit. Locks, covers and the alarm are never offered.
- Cha Dash reads the config, the entity states, and the energy meters' history, and changes nothing unless you list scripts for it to run or devices for it to control. Test connection says whether the token's user is an administrator.
- A token of an administrator works too, and it can also change the configuration and manage users. Test flags it.
- Revoke the token under that person's **Security** tab whenever you like; Cha Dash then asks to reconnect.
