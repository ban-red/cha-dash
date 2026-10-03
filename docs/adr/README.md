# Architecture decision records

> Every decision that shapes Cha Dash gets one short file here: what we decided, why, and what it costs. The roadmap's [decision table](../plan/04-roadmap.md#decisions) is the summary; these files hold the reasoning.

---

## How we record decisions

- **One decision per file**, named `NNNN-short-title.md` and copied from [template.md](template.md). Keep it short (20–50 lines) and link to the plan, a spike or a prototype for the detail.
- **Numbers follow the roadmap.** ADR-0001 to ADR-0018 match decisions 1–18 in the [decision table](../plan/04-roadmap.md#decisions). New decisions take the next free number. Numbers are never reused.
- **Every decision has a status:**

| Status | Meaning |
|---|---|
| **open** | We know we need this decision. It has a number and a line in the index below, but no file yet |
| **proposed** | Written up and under discussion in a pull request |
| **accepted** | Decided. Code and docs follow it |
| **superseded** | Replaced by a later ADR. The file stays, and its status links to the replacement |

- **Changing a decision.** Don't rewrite an accepted ADR. Write a new one that says which ADR it replaces, set the old one's status to `superseded by [ADR-NNNN](NNNN-title.md)`, and update the index. Typos and broken links can be fixed in place.
- **Proposing one.** Open a pull request with the new file at status `proposed` and a row in the index. It becomes `accepted` when it merges.

---

## Index

| ADR | Decision | Status | Date |
|---|---|---|---|
| [0001](0001-name-cha-dash.md) | Name: Cha Dash (`chadash`, `chadash-node`, `chadash.*` labels, `CHADASH_*` env) | accepted | 2026-10-03 |
| [0002](0002-licence-agpl-and-osi-limited-cla.md) | Licence: AGPL-3.0 with an OSI-limited CLA | accepted | 2026-10-03 |
| [0003](0003-frontend-vue-and-vite.md) | Frontend: Vue 3.6 and Vite | accepted | 2026-10-03 |
| [0004](0004-dogfood-the-maintainers-homelab.md) | Dogfood the maintainer's homelab | accepted | 2026-10-03 |
| [0005](0005-v0-1-scope.md) | v0.1 scope: the Front Page | accepted | 2026-10-03 |
| [0006](0006-owned-stacks-and-thin-node.md) | Cha Dash owns stacks; a thin node does the writing | accepted | 2026-10-03 |
| [0007](0007-docker-compose-first.md) | Docker Compose is the main install method | accepted | 2026-10-03 |
| [0008](0008-repo-and-images.md) | Repository and image names | accepted | 2026-10-03 |
| [0009](0009-default-port-3696.md) | Default port 3696 | accepted | 2026-10-03 |
| [0010](0010-unraid-stays-unraid.md) | Unraid stays Unraid | accepted | 2026-10-03 |
| [0011](0011-pwa-is-the-mobile-client.md) | The PWA is the mobile client | accepted | 2026-10-03 |
| [0012](0012-direction-a-themeable.md) | Visual direction: Instrument, built to be themeable | accepted | 2026-10-03 |
| 0013 | **Grid engine:** build our own layout engine, or adopt an existing grid library. Settled by a one-day comparison spike. [Roadmap](../plan/04-roadmap.md#still-open) | open | — |
| 0014 | **Node transport and identity:** mTLS with a pinned self-signed CA, or a Noise handshake. Needed before v0.2. [Roadmap](../plan/04-roadmap.md#still-open) | open | — |
| 0015 | **SQLite driver:** pure-Go modernc, or ncruces for encryption at rest. [Roadmap](../plan/04-roadmap.md#still-open) | open | — |
| 0016 | **Hardware floor:** amd64 and arm64 fully supported; how far armv7 goes. [Roadmap](../plan/04-roadmap.md#still-open) | open | — |
| 0017 | **AI/MCP stance:** embrace only through the action engine with human approval, or avoid. Needed before v0.2. [Roadmap](../plan/04-roadmap.md#still-open) | open | — |
| [0018](0018-go-module-path.md) | Go module path: `dash.cha.sh` | accepted | 2026-10-03 |
| [0019](0019-bun-for-javascript-tooling.md) | Bun for JavaScript tooling | accepted | 2026-10-03 |
| [0020](0020-node-deploys-with-compose-sdk-and-go-git.md) | The node deploys with the Compose SDK and go-git | accepted | 2026-10-03 |
