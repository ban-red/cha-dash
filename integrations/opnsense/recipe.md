---
title: An API key for a user with one privilege
summary: "A dedicated user with only Reporting: Traffic, the privilege behind the WAN counters and the live traffic stream. It's read-only, and it's everything this version reads."
access: read-only
params:
  - name: user
    label: User name
    default: chadash
    pattern: '^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$'
grants: ['Reporting: Traffic']
discover:
  - {scheme: https, port: 443, path: /, match: OPNsense}
---

## Create the user

1. **System → Access → Users**, then **+**.
2. Username `{{user}}`. Tick **Generate a scrambled password**: nobody signs in as this user.
3. Leave **Group membership** empty, then **Save**.

## Give it one privilege

1. Open `{{user}}` again and edit **Effective Privileges**.
2. Tick **Reporting: Traffic** and nothing else, then **Save**.

## Create its API key

Under **API keys**, press **+**. The browser downloads a text file with a `key=` and a `secret=` line: paste them into **Key** and **Secret**, then delete the file.

## Notes

- Test connection reads OPNsense's menu, which OPNsense filters by the user's privileges, so it shows exactly what the key opens. Anything beyond *Reporting: Traffic*, *Diagnostics: System Activity* and the *Lobby* pages is flagged as able to change things.
- Never add *Status: Services* (it restarts services), *Status: Interfaces*, *System: Firmware* or the DHCP pages: they all allow changes. *System: Gateways* would show gateway health, and it can also edit gateways, so this version doesn't ask for it.
- Prefer `https://` with TLS mode `tofu`. Over plain `http://` the key and secret cross the network on every request.
