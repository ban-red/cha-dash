# ADR-0027 · The Demo Home is a fictional home

**Status:** accepted · **Date:** 2026-10-28 · **Replaces:** one consequence of [ADR-0004](0004-dogfood-the-maintainers-homelab.md) ("the Demo Home simulator is seeded from its shape")

## Context

ADR-0004 made the maintainer's lab Cha Dash's first customer, and seeded the Demo Home simulator from its shape: its host nicknames, guests, container names, the last octets of its IPs and its day of incidents. That helped while the integrations were being written against that lab. It now costs more than it gives:

- The repo is public, and the roadmap puts a public demo on the Demo Home. Visitors would be browsing a copy of one real home.
- A demo that mirrors one lab shows one lab's integrations. Half the catalog never appears.
- Every change to the real lab tempts a change to the demo, and the other way round.

## Decision

**The Demo Home is a fictional family home, invented from scratch, shaped like what the integrations really publish.** It shares no names, addresses or story details with any real lab.

- The home: a router host, a three-node Proxmox cluster, a workstation, an Unraid NAS and two Docker hosts, all named after birds, on `10.20.30.0/24` and `*.home.example`. See [sim/README.md](../../sim/README.md).
- Integrations and incidents keep their real shapes: metric keys, attrs, incident IDs, scenarios, facts and wording follow the integrations' READMEs and specs, so the demo still exercises the same widget, Inbox and action paths.
- The incident kinds found in the dogfood lab stay, moved onto the fictional home, so their coverage stays.

ADR-0004 otherwise stands: the maintainer's lab is still the first customer, and its sanitized config is still the Homepage importer's acceptance fixture.

## Consequences

- The demo can be public without showing anyone's home.
- The demo uses more of the catalog (Jellyfin, AdGuard Home, SABnzbd, Seerr, Paperless-ngx, Audiobookshelf, Nextcloud, Speedtest Tracker), and new incidents can be written for them.
- The snapshots and every test that quotes the Demo Home changed at once. Stored layouts that name old host IDs show empty tiles until they're edited; a dev hub can start over with `bun run dev --fresh`.
- Real-lab surprises no longer reach the demo by themselves. When the lab finds a new kind of problem, it has to be written into the simulator on purpose.

## Alternatives considered

- **Rename the lab's hosts and keep the rest.** Cheap, but the story, sizes and container names would still be one real home.
- **Several real labs, blended.** More variety, but it needs other people's data and still leaks shapes.
