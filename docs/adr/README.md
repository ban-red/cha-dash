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
| [0013](0013-grid-engine.md) | Grid engine: build our own (`@chadash/layout` + `@chadash/layout-vue`), from the spike 2.4 comparison | accepted | 2026-10-03 |
| [0014](0014-node-transport.md) | Node transport and identity: mutual TLS 1.3 inside the WebSocket, Ed25519 keys pinned at enrollment, no CA | accepted | 2026-10-03 |
| [0015](0015-sqlite-driver.md) | SQLite driver: ncruces/go-sqlite3; Adiantum encryption at rest as an opt-in | accepted | 2026-10-03 |
| [0016](0016-hardware-floor.md) | Hardware floor: amd64 and arm64 fully supported, armv7 best-effort, 512 MB minimum | accepted | 2026-10-03 |
| [0017](0017-ai-and-mcp.md) | AI and MCP: agents read and propose, humans approve every write; off by default; MCP after 1.0 | accepted | 2026-10-03 |
| [0018](0018-go-module-path.md) | Go module path: `dash.cha.sh` | accepted | 2026-10-03 |
| [0019](0019-bun-for-javascript-tooling.md) | Bun for JavaScript tooling | accepted | 2026-10-03 |
| [0020](0020-node-deploys-with-compose-sdk-and-go-git.md) | The node deploys with the Compose SDK and go-git | accepted | 2026-10-03 |
| [0021](0021-git-sync-and-deploy-webhooks.md) | Stacks follow git through the deploy pipeline; webhooks only poke | accepted | 2026-10-22 |
| [0022](0022-oidc-and-forward-auth.md) | OIDC links to local users; the owner keeps a local way in | accepted | 2026-10-22 |
| 0023 | MCP timing: `mcp/v0` in v0.4 instead of after 1.0 ([09 · D1](../plan/09-roadmap-2026.md#7-decisions-this-needs)) | open | — |
| 0024 | A replacement for the archived CLA Assistant action ([09 · D2](../plan/09-roadmap-2026.md#7-decisions-this-needs)) | open | — |
| 0025 | The spec format public as `v1alpha1` before 1.0 ([09 · D3](../plan/09-roadmap-2026.md#7-decisions-this-needs)) | open | — |
| 0026 | "No enterprise tier" in the Pact ([09 · D4](../plan/09-roadmap-2026.md#7-decisions-this-needs)) | open | — |
| [0027](0027-demo-home-is-fictional.md) | The Demo Home is a fictional home, not a copy of the dogfood lab (replaces one consequence of 0004) | accepted | 2026-10-28 |
| [0028](0028-first-run-without-a-setup-token.md) | First run without a setup token: the first visitor creates the owner, named `admin` by default (replaces the setup token in 0005 and 0007) | accepted | 2026-10-29 |
| [0029](0029-short-passwords-and-address-bans.md) | Six-character passwords, backed by banning an address after five wrong passwords | accepted | 2026-10-29 |
| [0030](0030-board-flows.md) | Board flows: sections in bands (default), bento and mosaic, chosen per board (adds to 0013) | accepted | 2026-10-29 |
