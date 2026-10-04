# Cha Dash

**The front page of your home.**
One calm, beautiful page that tells you everything's fine — and lets you fix it, safely, when it isn't.

> Status: **planning** (October 2026). No code yet — the research and product plan are below. Repo: [github.com/ban-red/cha-dash](https://github.com/ban-red/cha-dash)

Cha Dash is an open-source, self-hosted dashboard for homelabs. Unraid, Docker and Proxmox are supported first-class. It starts as one customizable page of live widgets, and grows into safe control of your containers, stacks, VMs and LXCs, snapshots, updates and backups, across every host you run.

```bash
docker compose up -d   # the primary way to self-host Cha Dash (coming soon) — then open http://<host>:3696
```

## Docs

**Plan**
- [01 · Vision & product](docs/plan/01-vision.md) — thesis, personas, principles, feature map, signature experiences
- [02 · Experience & design language](docs/plan/02-experience.md) — visual system, motion, layout, widgets, editing, the safety ladder
- [03 · Architecture](docs/plan/03-architecture.md) — the Home Graph, integrations, owned stacks, the node, compose deployment, security, stack
- [04 · Roadmap, decisions & risks](docs/plan/04-roadmap.md) — milestones, decisions made and still open, risks
- [05 · Dogfood home](docs/plan/05-dogfood-home.md) — the real homelab Cha Dash has to win over first
- [06 · Next steps](docs/plan/06-next-steps.md) — the sequenced plan for finishing M0 (done)
- [07 · v0.1](docs/plan/07-v0.1.md) — waves 4–6 toward the Front Page release, and their shared contracts

**Decisions**
- [Architecture decision records](docs/adr/README.md) — one file per decision, with the open ones listed

**Design**
- [M0 prototypes](design/prototypes/README.md) — v2 in Direction A with the live theme engine; v1 with both directions
- [Design tokens v0](design/tokens/README.md) — theme documents, the OKLCH generator, validated status and contrast checks
- [Workbench list](docs/design/workbench.md) — every v0.1 widget × size × state

## The Pact (draft)

- **Open source (AGPL-3.0), built in the open** for the homelab community.
- **Your secrets never leave the server.**
- **Read-only until you say otherwise.**
- **No cloud, no phoning home.**
- **Your config and your stacks are yours.** Export config as YAML; your compose files live on your disks, in git.
- **Small and simple to run.** One `docker compose up -d`, under 100 MB of RAM.
- **Accessible.** Meeting WCAG 2.2 AA is required for every release.

## Contributing, security & license

- [CONTRIBUTING.md](CONTRIBUTING.md): how to help while Cha Dash is in planning
- [CLA.md](CLA.md): you keep your copyright, and contributions can only ever be released under OSI-approved licences
- [SECURITY.md](SECURITY.md): report vulnerabilities privately
- Licensed under [AGPL-3.0-or-later](LICENSE)
