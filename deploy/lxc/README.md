# Proxmox LXC script (draft)

A draft of a [community-scripts/ProxmoxVE](https://github.com/community-scripts/ProxmoxVE) script for Cha Dash, in their layout and style:

| File | Where it goes | What it does |
|---|---|---|
| [`ct/chadash.sh`](ct/chadash.sh) | `ct/chadash.sh` | Runs on the Proxmox VE host: asks for the container's settings through their `build.func` (Debian 13, unprivileged, 1 CPU, 1 GB RAM, 6 GB disk by default), makes the LXC and runs the install script in it. Its `update_script` moves an install to the newest release |
| [`install/chadash-install.sh`](install/chadash-install.sh) | `install/chadash-install.sh` | Runs inside the LXC: installs Docker Engine and its Compose plugin from Docker's apt repository, then Cha Dash with the release's own [`install.sh`](../install.sh) into `/opt/chadash` |

**It installs Cha Dash the official way:** the release's `install.sh` fetches that release's `deploy/compose.yaml` (the hub and chadash-node), pins the release in `compose.override.yaml` and starts it. Everything in [deploy/README.md](../README.md) then applies inside the LXC: `compose.override.yaml` for settings, `docker compose pull && docker compose up -d` (or the script's `update`) to upgrade, the backups. The hub is at `http://<LXC address>:3696`; create the owner account straight away.

**Docker inside an unprivileged LXC** needs the container's `nesting=1` and `keyctl=1` features; check that the submitted script turns both on through `build.func`'s settings. chadash-node sees the LXC's own Docker, not the Proxmox host's: add Proxmox VE itself in Settings → Integrations with an API token ([recipe](../../integrations/proxmox/recipe.md)).

## Status

**Not submitted.** community-scripts takes a new script only once the project has enough GitHub stars, and Cha Dash doesn't yet. When it does:
1. Check the scripts against their current template and contribution guide; `build.func`'s function names change from time to time.
2. Add the JSON file their website reads (`frontend/public/json/chadash.json`: name, description, resources, default credentials none, the port, the documentation link).
3. Try it on a test Proxmox VE host, then open the pull request there.

Both scripts are shellcheck-clean as bash. Nothing here runs in this repository's CI: they need their `build.func` and a Proxmox host.
