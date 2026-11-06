---
title: A local account with the View Only role
summary: A local UniFi account, restricted to local access, with View Only in UniFi Network and no access to the console's other apps. It can read devices, clients and health and change nothing.
access: read-only
params:
  - name: user
    label: User name
    default: chadash
    pattern: '^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$'
fills:
  username: '{{user}}'
grants: ['UniFi Network: View Only']
discover:
  - {scheme: https, port: 443, path: /, match: UniFi}
  - {scheme: https, port: 8443, path: /manage/account/login, match: UniFi}
---

## Create the account

1. On a UniFi OS console: **UniFi OS → Admins & Users → Create New**. On a standalone Network Application: **Settings → Admins & Users → Create New Admin**.
2. Tick **Restrict to Local Access Only**, so it's a local account: no UI.com sign-in and no two-factor prompt, which Cha Dash can't answer.
3. Username `{{user}}`, and a long random password. Paste the password into **Password**.

## Give it one role

1. Under the account's permissions, set **UniFi Network** to **View Only**.
2. On a console, give it no access to Protect, Access, Talk or the console's settings.
3. **Save**.

## Notes

- Test connection reads the account's role on the site back. *View Only* is read-only; *Site Admin*, *Admin* or a custom role is flagged as able to change things.
- Cha Dash signs in once and keeps the session; it signs in again only when UniFi ends it. UniFi OS limits failed sign-ins, so a wrong password stops being tried quickly and waits for you.
- Consoles serve a self-signed certificate. Use TLS mode `tofu` (trust on first use) or `pin` with its SHA-256 fingerprint; avoid `insecure`, which would send the password to anything that answers.
- Set **Platform** to *Standalone Network Application* for the Network Application installed on its own (Docker, a VM, usually on port 8443).
- This integration was written from UniFi's documented API shapes (Network Application 9.0.114 on UniFi OS 4.1.13), not tested against a real console yet. If something reads wrong, the repair names the endpoint.
