# Cha Dash for Home Assistant

The Home Assistant integration moved to a repository of its own, so HACS can install and update it: **[ban-red/ha-chadash](https://github.com/ban-red/ha-chadash)**.

It reads the hub's `GET /api/summary` with an `inbox.read` API token ([internal/README.md](../../internal/README.md)). That repository's CI reads the summary from the newest Cha Dash image every week, so a change here that breaks it shows there.
