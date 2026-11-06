---
title: A viewer's API token
summary: borg-ui has a viewer role, and a token acts as its user. A viewer's token lists the plans and runs of the repositories it may view and can't start or change anything. Cha Dash only reads.
access: read-only
discover:
  - {scheme: http, port: 8081, path: /api/system/info, match: borg_version}
---

## Add a viewer

In borg-ui, open **Settings → Users** and add a user, say `chadash`, with the role **viewer**. Give it **viewer** on every repository whose plans Cha Dash should show: a plan is listed only when the user may view all its repositories.

## Make its token

Sign in as that user, open **Settings → API tokens**, create a token and paste it (it starts `borgui_`) into **API token**.

## Say what it backs up

Set **Host it backs up** to the address of the machine borg-ui runs on (the URL's host is used otherwise). Its container mounts the host's `/` at `/local`, which **Path map** assumes; use `/:/` when borg-ui doesn't run in a container.

## Notes

- Written from borg-ui v2.4.6's source (`app/api/backup_plans.py`, `app/core/security.py`, `app/api/ssh_keys.py`), not checked against a running borg-ui yet.
- A remote (SSH) source covers its connection's host, read from the SSH connections list. A source on an agent's machine covers nothing yet: this API doesn't name the machine.
- Only the 50 newest runs are read, so a plan whose last run is older than that shows no result.
