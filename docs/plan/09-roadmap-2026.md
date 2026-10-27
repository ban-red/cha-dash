# 09 · Roadmap review, October 2026

> **Status: proposed** (2026-10-27). A review of where Cha Dash stands, what the homelab field shipped in 2025–2026, and the changes that follow for [04 · Roadmap](04-roadmap.md). The engineering fixes in [§ 2](#2-what-this-review-fixed) are already on the review branch. The milestone changes in [§ 5](#5-the-revised-milestones) and the decisions in [§ 7](#7-decisions-this-needs) take effect when agreed: as rows in the decision table, ADRs where they change one, and edits to 04.

---

## Summary

- **Cha Dash's engineering is ahead of the field.** Its safety work is unmatched in this space: tiered actions with blast radius and step-up, a hash-chained audit log, an authorization matrix tested over every route, and a node over mutual TLS. So is its supply-chain work: SHA-pinned actions, cosign, SBOMs and provenance. Competitors charge for less (Pulse Pro, Dockhand Enterprise, Portainer BE).
- **The field moved toward Cha Dash's thesis in 2026.** Homepage 2.0, Homarr 2.0, Dozzle 11 and Pulse all shipped MCP. Dozzle, Arcane and Dockhand shipped safe updates with rollback. Watchtower was archived. **None of them gates an agent's writes behind a human approval**, and Cha Dash's engine was built to do exactly that.
- **Six changes would most improve Cha Dash's position:**
  1. OIDC before v0.3.
  2. A minimal Update Center in v0.3.
  3. A docs site and public demo in v0.3.
  4. A public spec format and community repo in v0.3.
  5. MCP, read and propose, in v0.4.
  6. Message IDs for the hub's sentences now, so i18n isn't a rewrite at 1.0.
  Each is in [§ 5](#5-the-revised-milestones), with its evidence.
- **This review found and fixed real defects:**
  - 14 reachable Go standard-library vulnerabilities.
  - Two ordering races in the action engine (a task's stored state, and the cancel's audit record).
  - A wrong failure message in spec verify.
  - A 32-bit sort bug that kept CI's armv7 job red.
  - An IPv6-dependent SSRF guard.
  It also added the missing tooling: golangci-lint, `go fix`, oxlint and ESLint, Renovate, zizmor, CodeQL, Scorecard and nightly fuzzing. Details are in [§ 2](#2-what-this-review-fixed).

---

## 1. Where Cha Dash stands

| | |
|---|---|
| **Released** | 0.1.0-beta.1, the read-only Front Page (2026-10-04) |
| **In progress** | v0.2 "Control". Waves 7–10 are merged and the review fixes are in. What's left: the live runs on named test targets, the node's idle RSS against a real daemon, then 0.2.0-beta ([08 · v0.2](08-v0.2.md#the-wave-8-and-9-merge-2026-10-06)) |
| **Code** | ~95k lines of Go plus ~54k of Go tests; ~47k of TypeScript and Vue with 879 tests; 25 Tier 0 integrations and 9 bundled specs; 20 ADRs |
| **CI** | gofmt, vet, `-race` on amd64 and arm64, 32-bit, budgets (initial JS, image size, first paint, diff rate), axe over every view, multi-arch images, signed releases |

**What's already best-in-class**, and should be marketed as such:
- **The safety ladder.** Undo, hold-to-confirm with blast radius, typed confirmation with step-up, and self-aware T4 when an action would take down the hub or the router.
- **The trust chain.** One action engine for every surface, an append-only hash-chained audit, a route matrix test, and the node's mTLS with pinned keys and a local policy the hub can't widen.
- **Footprint.** A ~6 MB image, against Proxmox Datacenter Manager's dedicated ~4 GiB VM and Homarr's memory-use bounty.
- **No paywall on the serious features.** Roles, audit and step-up are in the AGPL core.

---

## 2. What this review fixed

All of these are on the review branch as separate commits, each with its reasoning in the commit message.

### Defects

| Fix | What was wrong | Found by |
|---|---|---|
| **Go 1.27.3, x/net v0.61.0** | govulncheck found 14 standard-library vulnerabilities the code reaches: compromised x509 intermediates, HTTP/1 and HTTP/2 desync and exhaustion, unbounded Range headers, an `html/template` escaping bug. CI's govulncheck would have failed on `main` | govulncheck v1.9.0 |
| **The action engine stores a task's end before showing it finished** | A just-finished task trimmed from the live set read back as "verifying". A hub stopped in that window marked a succeeded task as interrupted, adding a second, contradictory audit record. `TestPlanApproveRun` failed intermittently | A stress run (`-count=40 -cpu 1,4`) |
| **A cancel is audited before the end it causes** | Cancel signalled the task and then audited itself, so the cancelled task's "failed" record could land first, and the log read as failing before anyone cancelled it. `TestFailures` failed once in a full `-race` run | The final full `-race` run |
| **Spec verify fails with the spec's sentence** | When the deadline cut a read after earlier reads had answered, the task said "didn't answer in time" instead of the spec's message. A new test reproduces it deterministically | The full `-race` run |
| **32-bit script validation** | `int(a.from - b.from)` truncated nanosecond durations on 32-bit, so two incidents two days apart were refused as overlapping. CI's armv7 stand-in (`GOARCH=386`) was red on `main` | `GOARCH=386 go test` |
| **One outbound dial guard for icons and notify** | The address check ran after `socket()`, so on a host without IPv6 a refused `[::1]` failed with a different error, and notify retried it four times | A test failing in an IPv6-less container |
| **The node writes its own files through `os.Root`** | Not exploitable today (names are pattern-checked), but the confinement now comes from the OS rather than from every caller | gosec's taint analysis (G703) |
| **`rows.Err()` after reading approvals and due deliveries** | A read cut short ended silently | rowserrcheck |

### Tooling

| Added | Notes |
|---|---|
| **golangci-lint v2.15** (`.golangci.yml`) | Bug-finding linters only, at zero findings; every exception carries its reason. Replaces the standalone staticcheck step |
| **`tools/go.mod`** | golangci-lint and govulncheck as `tool` directives, built with the repo's Go. staticcheck via `go run …@v0.8.2` falls back to Go 1.26 and can't read this module |
| **`go fix` gate** | The tree is on the Go 1.27 modernizers (`errors.AsType`, `new(expr)`, …), and CI fails on a non-empty `go fix -diff`. One analyzer rewrite didn't compile and was fixed by hand |
| **oxlint + ESLint 10** (`bun run lint`) | The create-vue setup: oxlint first, then eslint-plugin-vue for templates. The JS workspaces had no linter at all |
| **Renovate** (`renovate.json5`) | Nothing updated the SHA-pinned actions or exact-pinned packages. Three-day `minimumReleaseAge` everywhere; grouped Go, Bun, actions and linter PRs; custom managers for `BUN_VERSION`, `COSIGN_VERSION` and `ZIZMOR_VERSION`. **Needs the Renovate app installed** |
| **Bun install cooldown** (`bunfig.toml`) | `minimumReleaseAge = 3 days`, the main defence against npm worms of the Shai-Hulud kind |
| **zizmor** (Workflows job) | `ci.yml` and `release.yml` are clean. `cla.yml`'s triggers are accepted with their reason in `.github/zizmor.yml` |
| **CodeQL** (Go, TypeScript, Actions) and **OpenSSF Scorecard** | Both new workflows, SHA-pinned |
| **Fuzz targets** and a nightly job (`fuzz.yml`) | `icons.FuzzCheck` (SVGs off the network, with an independent safety oracle), `jsonpatch.FuzzCompact` (Compact's contract, appends included) and `homepage.FuzzBuild` (pasted YAML). The seed corpora run in every `go test`; the nightly job fuzzes each for 5 minutes |
| `.editorconfig` | What gofmt and the linters expect |

### Found, not fixed: needs a decision

- **The CLA bot's action is archived upstream** (`contributor-assistant/github-action`). It still works, but nobody will fix it. See [D2](#7-decisions-this-needs).
- **Branch protection should require the new checks**: Workflows, CodeQL, and the JavaScript job's lint step.
- **The Docker `web` stage installs the root's lint packages too.** That costs build time only, not image size. `bun install --filter @chadash/web` would skip them.

---

## 3. The field in 2026

GitHub stars as of 2026-10-27. Sources are at the [end](#sources).

| Project | Stars | What matters for Cha Dash |
|---|---|---|
| Uptime Kuma | 93.0k | 2.0 stable. 90+ notification providers is the bar people compare against |
| Portainer CE | 38.7k | Pitches CE at homelabs; RBAC, SSO and GitOps are paid |
| Glance | 37.8k | A single Go binary under 20 MB; a **community-widgets repo of about 165**, vetted by maintainers |
| Homepage | 33.4k | About 164 widgets. **2.0 (Aug): built-in auth and MCP**; OIDC auto-login in 2.3; weekly releases |
| community-scripts/ProxmoxVE | 29.9k | 700+ one-command LXC installs: *the* Proxmox distribution channel |
| Beszel | 26.6k | A tiny agent; in 2026 added SMART, ZFS, systemd and container-health alerts, and image-update flags. Its top request is PVE guest stats |
| Dockge | 24.7k | **Stalled.** Its top issue asks whether it's still developed, and users are moving to Arcane and Dockhand |
| Pangolin | 23.2k | A 2025 breakout: tunnels plus SSO plus a launcher |
| Dozzle | 14.7k | **v11 (Sep): OIDC, safe updates with rollback, auto-update, read-only MCP over OAuth**; AI chat is paid |
| Komodo | 12.8k | 2.0: outbound agents, PKI and onboarding keys; git stacks; resource sync as TOML |
| Arcane | 8.1k | Releases every few days; free OIDC, RBAC and passkeys; Trivy, GitOps, delayed auto-updates, backups. No MCP |
| Pulse | 6.9k | Agentless PVE and PBS; "Patrol" AI and an MCP adapter; **RBAC, audit and history are Pro** |
| Dockhand | 6.9k | Public since Dec 2025, +9% in a month; git deploys, scan-gated updates, restic. **BSL**, with RBAC and audit enterprise-only |
| Homarr | 5.2k (+7.2k archived) | **2.0 (Oct): Workshop (community widgets), MCP "integration requests"**, 31 new integrations |
| Proxmox Datacenter Manager | — | 1.0 in Dec 2025, 1.1 in May 2026 (cross-remote guests, central updates). Needs a dedicated ~4 GiB VM |
| Watchtower | — | **Archived Dec 2025** |

**What the field tells us**
1. **Agents arrived, but without brakes.** Four leading tools shipped MCP in 2026. Their writes are either config edits (Homepage) or paid "fixes" (Pulse Pro). Nobody puts a human approval on an agent's write. Cha Dash's propose-and-approve engine, with T2 token plans already waiting in the Inbox, is the one design built for it.
2. **Safe updates are the battleground.** Watchtower is gone. In the selfh.st 2025 survey, 1,855 people update by hand against 600 automatically. Three Docker managers shipped update-with-rollback this year. Cha Dash already has the hard part: a health-gated deploy that rolls back on its own.
3. **Auth is table stakes.** Homepage, Glance and Dozzle all added auth or OIDC in 2025–26. The survey's auth tools are Authelia (390), Pocket ID (310) and authentik (268).
4. **Breadth comes from the community.** Glance's community repo and Homarr's Workshop show the pattern, and "widget for X" dominates every feature board. Cha Dash's Tier 1 specs are the right tool, but the format stays internal until 1.0.
5. **Presence matters.** Every leader has a docs site: VitePress (Beszel, Dozzle), Starlight (Arcane), Docusaurus (Komodo, Dashy), Zensical (Homepage). Homarr, Dashy, Komodo and Pulse all run public demos. Abandonment is punished fast (Dockge), and so is opacity: users distrust "AI-coded" infrastructure tools.
6. **Proxmox users are the core.** About 45% of survey respondents run Proxmox. PDM's footprint and Beszel's top request both leave room for a light, correlated Proxmox view.

---

## 4. Where Cha Dash wins

The positioning, sharpened by § 3: **the one dashboard that can safely act, for people who run Proxmox, Unraid and Docker together, with nothing behind a paywall.**

- **Safe control, end to end.** Every write has a plan, a blast radius, a tier, an audit record and, for deploys and updates, an automatic rollback.
- **Agents with brakes.** Agents read and propose, and a human approves from their phone, with the same ring and the same audit.
- **One graph across platforms.** "Rebooting pluto takes down the router" is a sentence only a correlated graph can say.
- **Small.** About 6 MB and one compose file. Say so on the front page.
- **No enterprise tier.** Roles, audit, SSO and step-up stay in the AGPL core ([D4](#7-decisions-this-needs)).

---

## 5. The revised milestones

Changes against [04 · Roadmap](04-roadmap.md), marked **➕ added**, **⬆ pulled forward** or **⬇ deferred**. The dogfood rule and every Done-when in 04 still apply.

### v0.2 · Control: finish as planned
- The live runs on named targets; the node's idle RSS; then **0.2.0-beta.1**.
- ➕ Merge this review's branch first, so the beta ships on Go 1.27.3 with CI green on every leg.

### v0.2.x · Before v0.3, no longer optional
- ⬆ **OIDC and forward-auth**, from "can slip" to required. Tested against Pocket ID, Authelia and authentik. Passkey step-up stays on top of OIDC sessions.
- ⬆ **The public OpenAPI 3.1 document, generated from the route table** (`authz/routes.go`). A test fails on any route without a schema, the same way `TestEveryRouteHasARow` works today. It's the prerequisite for MCP, the docs site and third-party clients.
- Homarr and plain-YAML importers, as planned. Homarr 2.0 users are the likeliest migrants.
- Apprise, email and Home Assistant notifications, as planned.

### v0.3 · Fleet, plus updates and reach
Everything in 04's v0.3 (remote nodes, fleet stacks, Graph v2, Atlas, history), plus:
- ⬆ **Update Center, minimal.**
  - Image updates detected through the nodes.
  - Update one container or stack with the existing health gate and automatic rollback.
  - Hold or snooze an update.
  - Staged, dependency-ordered rollouts stay in v0.4. This answers Watchtower's end with the strongest thing Cha Dash already has.
- ⬆ **Docs site** on VitePress (Vue, like the app), with install, integrations generated from the manifests and recipes, the API from the OpenAPI document, and the security model.
- ⬆ **Public demo** on the Demo Home simulator: sign in automatically as an Operator, and every action runs against the simulator only. The simulator already scripts the incidents, so this mostly needs hosting and a read-only switch.
- ⬆ **Spec format `v1alpha1` and a community spec repo.** Schema-checked, reviewed, installable from Settings, with recorded fixtures required. "Alpha" means the format can still change, with a migration. This is where breadth comes from.
- ⬆ **Published threat model**, from v1.0. The external review stays at 1.0.
- ➕ **Message IDs for the hub's sentences.**
  - The hub returns about 250 English sentences (refusals, preconditions, recipes) that the web shows as is.
  - Give each a stable ID with parameters now (`{"code", "message", "id", "args"}`), keeping `message` as the English fallback.
  - At 1.0, translation is then a catalogue in Weblate, not a rewrite of every handler.
- ➕ **Distribution.** A community-scripts LXC script (once their star threshold is met), the TrueNAS app listing (already v0.3), and a signed one-line installer.
- ➕ **Integrations that are sources, not rivals.** 04 already plans Beszel as a source and Dozzle deep links for v0.3. Add Komodo, Arcane and Dockhand (the stacks they manage, read-only) and ⬆ Gatus (checks, from v1.0). People already run these. Reading them makes Cha Dash more useful, not less.

### v0.4 · Care, plus agents
Everything in 04's v0.4 (the full Update Center, backups, incident timeline), plus:
- ⬆ **MCP, read and propose** ([D1](#7-decisions-this-needs)):
  - Graph and incidents as resources; `search`, `get_resource`, `plan` and `propose` as tools.
  - Writes only as proposals that wait in the Inbox for an approver's ring and step-up. T2 token plans already work this way.
  - Off by default, versioned as `mcp/v0` so it doesn't freeze the API.
  - Works with local models, since Cha Dash ships and calls no model.

### v1.0 · Home: what's left
- Spec format `v1` (from `v1alpha1`), translations through Weblate on the message IDs, the external security review, stable config and API guarantees, wall and TV modes, Helm and Umbrel.
- ⬇ The docs site, demo and threat model are no longer 1.0 work. They're done by then.

---

## 6. Engineering track

Runs alongside every milestone. ✅ marks what this review did.

| # | Item | Why | When |
|---|---|---|---|
| E1 | ✅ golangci-lint, `go fix` gate, tools module, govulncheck 1.9 | Bugs found, not taste; tools built with the repo's Go | Done |
| E2 | ✅ oxlint and ESLint; ✅ Renovate; ✅ Bun cooldown; ✅ zizmor, CodeQL, Scorecard | Supply chain: the npm worms and the 2026 tag-rewrite attacks on actions | Done; **install the Renovate app** and **require the new checks** |
| E3 | ✅ Fuzz targets and a nightly job → **more targets** | Parsers that take untrusted bytes. Done: icons, jsonpatch, the Homepage importer. Next: `nodeproto` frames before pairing, the export importer's sealed bundle, spec templates | v0.2.x |
| E4 | **Browser end-to-end tests** with Playwright 1.64 against `chadash serve --demo` with the built SPA, plus `@axe-core/playwright` | happy-dom can't catch layout, focus or real WebSocket bugs. Use the stable "stories + gallery" `mount`, not the discontinued experimental CT packages | v0.2.x |
| E5 | **Visual regression** for the Workbench with Vitest 5 browser mode `toMatchScreenshot`, baselines generated only in CI | 04's risk "design quality slips as contributors add widgets" | v0.3 |
| E6 | **A flake budget**: a nightly `-count=20 -cpu 1,4` job over `action`, `realtime`, `notify`, `spec` and `nodes` | This review found four timing bugs that one run in CI doesn't show | v0.2.x |
| E7 | **SLSA Build L3**: images built in a reusable workflow, `actions/attest` with `push-to-registry`, immutable releases, `gh attestation verify --signer-workflow` in the release notes | cosign and provenance are already in; L3 is a small step from here | v0.3 |
| E8 | **Workflow hardening**: `cache-mode: none` on the release jobs; try `actions.lock` (public preview) beside the SHA pins | The May 2026 TanStack attack went through a poisoned Actions cache | v0.2.x |
| E9 | **`/metrics`** (Prometheus) on a separate, off-by-default listener; never `pprof` on the main one | Homelabbers run Prometheus and Grafana; it's also what a footprint claim is checked with | v0.3 |
| E10 | **Vue 3.6 stable** when it lands (rc.13 now); **TypeScript 7** once typescript-eslint and vue-tsc support it (Renovate holds it below 6.1) | Planned in ADR-0003; RC churn is already a listed risk | When upstream is ready |
| E11 | **Release cadence**: a beta every 2–3 weeks while 0.x, each with its changelog | Dockge's stall cost it its users, and Arcane's cadence wins mindshare. The hand-written, user-facing changelog is a strength: keep it, don't generate it | Ongoing |

---

## 7. Decisions this needs

Each becomes a row in 04's "Still open" table now, and an ADR when settled. Numbers follow the decision table.

| # | Question | Recommendation |
|---|---|---|
| **D1** (21) | **When does MCP ship?** ADR-0017 says after 1.0, so it doesn't pin an unstable API | **v0.4, as `mcp/v0`:** read and propose only, off by default, generated from the OpenAPI document. A versioned adapter doesn't freeze the API, and waiting cedes "agents with brakes" to tools without brakes. Supersede only the timing in ADR-0017; its rules stay |
| **D2** (22) | **What replaces the archived CLA Assistant action?** | Options: the hosted cla-assistant.io GitHub App (no workflow, so the zizmor exception goes too); EasyCLA; a small in-repo check. ADR-0002's OSI-limited CLA stays. Only the bot changes |
| **D3** (23) | **Publish the spec format before 1.0?** | **Yes, as `v1alpha1` in v0.3**, with a versioned schema and migrations, so the community repo can start |
| **D4** (24) | **Pledge "no enterprise tier" in the Pact?** | **Yes:** "Roles, audit, SSO and every safety feature are in the open-source core. There is no paid edition." Competitors' paywalls make it a differentiator, and the AGPL plus the CLA already allow it |

---

## 8. Risks: updates to 04

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| **Competitors ship control first** (Homepage, Homarr and Dozzle all grew actions or MCP in 2026) | High | High | Lead with what they can't copy quickly: the safety ladder, the correlated graph, owned stacks with rollback. Ship the v0.3 Update Center |
| **The r/selfhosted "new project Friday" rule** (seen only in a search snippet) | Medium | Medium | Post the v0.2 demo moment on a Friday; let the repo's age pass the threshold first |
| **Upstream tooling archived under us** (the CLA action now; Watchtower for others) | Medium | Medium | Renovate and zizmor's `archived-uses` audit flag it early; D2 |
| **Flaky tests erode trust in CI** | Medium | Medium | E6. Four timing bugs found in this review, all in real code, not only tests |

---

## Sources

Fetched on or before 2026-10-27. Reddit was reached only through mirrors.

**Projects**
- Homepage releases: https://github.com/gethomepage/homepage/releases
- Homarr 2.0: https://newreleases.io/project/github/homarr-labs/homarr/release/v2.0.0
- Homarr Workshop: https://v2.preview.homarr.dev/docs/management/workshop/
- Dozzle releases: https://github.com/amir20/dozzle/releases
- Dozzle MCP: https://dozzle.dev/guide/mcp
- Komodo releases: https://github.com/moghtech/komodo/releases
- Arcane releases: https://github.com/getarcaneapp/arcane/releases
- Pulse: https://github.com/rcourtman/Pulse
- Dockhand: https://noted.lol/dockhand
- Dockge #893: https://github.com/louislam/dockge/issues/893
- Glance community widgets: https://github.com/glanceapp/community-widgets
- Beszel releases: https://github.com/henrygd/beszel/releases
- community-scripts: https://github.com/community-scripts/ProxmoxVE
- Watchtower's end: https://linuxiac.com/docker-update-tool-watchtower-reaches-end-of-maintenance/
- Proxmox Datacenter Manager 1.0: https://www.virtualizationhowto.com/2025/12/proxmox-datacenter-manager-just-went-ga-and-it-changes-everything/

**Surveys and opinion**
- selfh.st 2025 survey data: https://github.com/selfhst/cdn/blob/main/assets/surveys/annual/2025-results.json
- Dashboard fatigue: https://www.virtualizationhowto.com/2026/06/why-i-never-bought-into-the-home-lab-dashboard-trend/

**Supply chain**
- CISA on Shai-Hulud: https://www.cisa.gov/news-events/alerts/2025/09/23/widespread-supply-chain-compromise-impacting-npm-ecosystem
- TanStack cache poisoning: https://orca.security/resources/blog/tanstack-npm-supply-chain-worm/
- ChainDrop: https://securitylabs.datadoghq.com/articles/npm-worm-compromises-popular-npm-packages/
- Compromised actions in 2026: https://thehackernews.com/2026/09/compromised-github-actions-came-back.html
- checkout v7 and `pull_request_target`: https://github.blog/changelog/2026-06-18-safer-pull_request_target-defaults-for-github-actions-checkout/
- Actions `cache-mode`: https://github.blog/changelog/2026-09-10-control-github-actions-cache-access-with-cache-mode
- Workflow lockfiles: https://github.blog/changelog/2026-10-14-github-actions-workflow-lockfiles-in-public-preview/

**Tooling**
- Renovate presets: https://docs.renovatebot.com/presets-config/
- Renovate gomod manager: https://docs.renovatebot.com/modules/manager/gomod/
- Bun's `minimumReleaseAge`: https://bun.com/docs/runtime/bunfig
- golangci-lint changelog: https://golangci-lint.run/docs/product/changelog/
- Go 1.27: https://go.dev/doc/go1.27
- oxlint JS plugins: https://oxc.rs/docs/guide/usage/linter/js-plugins.html
- Vitest visual regression: https://vitest.dev/guide/browser/visual-regression-testing
- MkDocs Material maintenance and EOL: https://squidfunk.github.io/mkdocs-material/changelog/
