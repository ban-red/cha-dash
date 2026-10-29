# ADR-0004 · Dogfood the maintainer's homelab

**Status:** accepted · **Date:** 2026-10-03 · **Roadmap:** [decision 4](../plan/04-roadmap.md#decisions)

## Context

A dashboard designed against imaginary homelabs gets the details wrong: which numbers matter, which errors are noise, what "several hosts" really looks like. We need one real, messy home to win over first. The maintainer's lab is described in [05 · Dogfood home](../plan/05-dogfood-home.md).

## Decision

**The maintainer's homelab is Cha Dash's first customer.** Every milestone has to work on it before it ships, and **v0.1 is done when the Homepage it runs today is switched off** and nobody wants it back.

The lab, by nickname:

| Host | Platform | Role |
|---|---|---|
| pluto | Proxmox VE | Network core: the OPNsense router VM |
| minig5 | Proxmox VE | Home Assistant and Frigate |
| mars-1, mars-2 | Proxmox VE, 2-host cluster | `pvedocker`, a Docker LXC: Traefik, Uptime Kuma, Linkding, Karakeep |
| io | Proxmox VE | Workstation VMs |
| jupiter-nas | Proxmox VE | NAS and miscellaneous |
| jupiter | Unraid | Main app host: Plex, the *arr apps, qBittorrent, Mealie, Immich, and PBS in a container |

That's 5 active Proxmox endpoints, one Unraid box and two Docker hosts, both reached through socket proxies.

## Consequences

- **It sets the v0.1 integration list** ([ADR-0005](0005-v0-1-scope.md)).
- Multiple PVE instances are the normal case, not an edge case. Nodes must run inside LXCs. The router is a VM, so blast radius has to warn when an action takes down the network.
- Its real problems become test cases: a revoked Plex token, a Karakeep API change, a PBS datastore at 92% with a failed task, a host at 95% memory.
- A sanitized copy of its config is the Homepage importer's [acceptance fixture](../plan/05-dogfood-home.md#importer-acceptance-test), and the Demo Home simulator is seeded from its shape. *Replaced by [ADR-0027](0027-demo-home-is-fictional.md): the Demo Home is now a fictional home.*
- **Privacy:** the repo is public. Docs and fixtures use host nicknames only, never domains, IPs, personal names or secrets.
- One lab can bias the product. Launch-breadth integrations and 10 external daily beta users (a v0.1 exit criterion) balance it.

## Alternatives considered

- **A synthetic reference lab only.** Easy to control, but it never surprises you.
- **Several beta labs from day one.** More coverage, but slower feedback and no single "switch the old one off" moment.
