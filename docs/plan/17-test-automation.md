# 17 · No manual tests: every check automated

> **Status: agreed** (2026-11-05). The maintainer set the rule, and settled [T1–T4](#open-questions-t1t4) as recommended ([Decisions](#decisions)): **nothing is tested by hand.** Every check a plan needs is an automated test that the agent writes, runs and reads. This plan sets out the method, the harness it needs, and what replaces [14b's manual tests](14b-v0.4-manual-tests.md).

## The rule

1. **A check is a test, or it doesn't count.** No plan has a "manual tests" section or a checklist for a person. A plan's "Done when" names the automated tests that prove each line.
2. **The test comes with the change**, in the same commit, at the lowest layer that can prove it ([layers](#layers)).
3. **If a check looks impossible to automate, build the harness.** A fake, a simulator mode, a test clock, a recorded exchange. "Needs a person", "needs a phone" or "needs a week" is a reason to build something, not to write a checklist.
4. **The lab is never a test target.** Tests run on throwaway things the test makes and removes: data directories, containers, registries, networks. The lab is only read, to check that recorded fixtures still match the real thing ([layer 5](#layers)).
5. **A test that fails one run in fifty is a bug** (E6). No retries in CI. The nightly stress job grows to cover the new layers.

## Layers

Each layer runs everything below it too. Pick the lowest that proves the claim.

| # | Layer | What it uses | Where it runs | Command |
|---|---|---|---|---|
| 1 | **Unit and fixture** | Go and Vitest, recorded fixtures, injected clocks | Anywhere, every commit | `go test ./internal/foo/`, `bunx vitest run …` |
| 2 | **Contract and simulator** | `integrationtest` against fixtures; the Demo Home simulator (`sim/`) for scenarios across sources | Anywhere, every commit | `go test ./integrations/…`, `go test ./internal/demo/` |
| 3 | **Engine** (new) | A real Docker Engine: the node's verbs, stacks, the Compose SDK, a throwaway registry. Containers carry the label `chadash.test=<run id>` and are removed afterwards, even after a failure | CI on every pull request; locally when Docker is running ([T1](#open-questions-t1t4)) | `bun run test:engine` → `go test -tags engine ./internal/node/... ./integrations/docker/` |
| 4 | **System** (grows) | The real hub and a real node as processes, a real Docker Engine, Playwright in Chromium and WebKit, test automation mode, a test clock, fake outside services | CI on every pull request | `bun run e2e` (Demo Home, as today), `bun run e2e:system` (hub + node + Docker) |
| 5 | **Live drift** | `spikes/live-verify` promoted to `tools/live`: read-only requests to the lab's real instances, sanitized, compared with the recorded fixtures | Run by the agent when an integration changes or before a release ([T2](#open-questions-t1t4)) | `bun run live:verify [id…]` |

Layer 5 isn't a test of Cha Dash. It finds out when an upstream API has changed so the fixtures are wrong. When it finds a difference, the fix is new fixtures (recorded by `chadash dev record`, sanitized) and a layer 1–2 test that fails on the old ones.

## The harness to build

What's missing for layers 3 and 4, and for checks that once needed a person, a device or time.

### H1 · Engine tests (layer 3)

- A `engine` build tag and `internal/testengine`: connects to `DOCKER_HOST`, skips with a clear message when there's no engine (fails in CI, where `CHADASH_REQUIRE_ENGINE=1`), and gives each test a run ID, a labelled network and cleanup that removes everything with its label.
- **A throwaway registry** (`registry:2`) on a random loopback port, optionally behind htpasswd for private-registry tests.
- **Images made without a build.** A canary is a pinned busybox with another config (entrypoint, health check, a label for its version), committed through the Engine API and pushed to that registry: one that serves HTTP and passes its health check, one that exits 1 at once, one whose health check never passes. Pushing a new version moves the tag. This replaces "a canary from a temporary registry on pvedocker", needs no `docker build` and no new dependency.
- **Built** (2026-11-05): `internal/testengine`, `bun run test:engine`, the Engine CI job on amd64 and arm64, and the first engine tests: stacks deploy, rollback (never healthy, exits at once), the dry run blocking a missing tag, a failed new stack removed; `container.update` with each broken image and a good one; `image.check` seeing the tag move.
- Images the tests pull from Docker Hub (`registry:2`, `busybox`) are pinned by digest.

### H2 · A test clock

The places that take a `now func() time.Time` get it from one clock made in `newApp`. With test automation on, and only then, `CHADASH_TEST_CLOCK=1` makes that clock controllable: `POST /api/test/clock {"advance": "15m"}` or `{"set": "…"}`, with the automation key. Timers run through the same clock.

**Built** (2026-11-05): `internal/clock` (real time plus a skew that only moves forward; tickers, timers and `AfterFunc` wake on each move), the flag and the route, and the clock wired through `newApp` into every service with a `Now` and the schedulers' loops (notify, updates, the incident recorder, the action engine's expiry, the Demo Home). Tests: approval expiry and a scheduled rollout's window happen when the clock moves, with no direct call. Sessions, step-up, tokens, rate limits and transport timeouts stay on real time. This automates maintenance windows, the 5-minute grace, the morning digest at its time, the public demo's hourly reset, token expiry, and "a week of recording" (the simulator generates a week in seconds).

### H3 · Fake outside services

A small set of fakes in `internal/testfakes`, each a Go `httptest`-style server with a capture log the test reads:
- **Notifications:** the channels Cha Dash has, ntfy and webhooks, through one capture server (`testfakes.Capture`). A test asserts what was sent and what wasn't. Gotify or email get a fake when they become channels.
- **Web Push:** a push service that checks the VAPID signature and decrypts the payload (RFC 8030, 8291, 8292), so a test reads the digest's text exactly as a phone would receive it.
- **Webhooks in:** PVE notification payloads and git forge push events, replayed from recorded fixtures with the right signatures.
- **OIDC:** a minimal provider for the sign-in flows.

**Built** (2026-11-05): `internal/testfakes` with `Capture` and `Push` (VAPID checked, payload decrypted as a browser does). notify's own tests now use its device and VAPID check, so the fake is proven against the real sender. `TestDigestArrivesAtItsTimeOnTheHubClock` runs the digest through `Run` on the test clock to an ntfy capture and a phone. Still to build: webhooks in, and the OIDC provider. A whole hub sending Web Push to a loopback fake needs a test-only switch, added with `e2e:system`.

### H4 · Passkeys without a person

Playwright's Chromium gets a virtual WebAuthn authenticator (CDP `WebAuthn.addVirtualAuthenticator`, user verification on). The owner registers a passkey and steps up with it in the browser, through the real code path, with nothing lowered. Test automation mode stays for scripts that don't drive a browser.

### H5 · Agents through MCP

A scripted MCP client in the system tests, using the official TypeScript SDK as a test-only dependency: initialize, list tools, `propose`, `get_resource`, `plan`, `withdraw`, each refusal. It's the same protocol Claude Desktop speaks, so "two real clients" becomes a protocol test plus a conformance check against the SDK's version matrix. The approval is done in the browser with the virtual passkey (H4).

### H6 · Phones

Playwright's WebKit project with iPhone and iPad sizes beside Chromium's Pixel, the PWA's install manifest and service worker checked, and offline behaviour tested with the network cut. A real handset adds nothing a test can't see, so there's no device test.

### H7 · Install and upgrade

- **Installer:** CI runs `install.sh` in a clean `debian:trixie` container with Docker-in-Docker, against a release built in the same job: digests pinned, a tampered `compose.yaml` and a wrong-tag signature refused.
- **LXC script:** runs against a `pct`/`pvesh` shim that records the calls and checks them against a fixture; what it does inside the CT runs in a Debian container.
- **Upgrades** (already in v0.5): the current build on 0.3.0 and 0.4.0 data directories.

### H8 · The agent's loop

What the agent does on every change, written into its local instructions and a local skill ([Agent tooling](#agent-tooling)):
1. Write the test first, at the lowest layer. Watch it fail.
2. Make the change. Run layers 1–2 for what changed; layer 3 when a node or Docker path changed and an engine is available.
3. Layer 4 and the full matrix run in CI. When the maintainer pushes, the agent reads the results (`gh run view`), and a red result is the agent's to fix.
4. Never write "check by hand" in a plan, a PR or a commit. If a check can't be written yet, the missing harness piece becomes a task in the current wave.

## Replacing 14b

Each open item in [14b](14b-v0.4-manual-tests.md), and where it goes.

| 14b | Check | Automated as | Layer |
|---|---|---|---|
| 1 | A rollout scheduled into a window runs in it, incidents don't notify, the leftover one notifies at the end plus grace | Test clock (H2), notification fake (H3), canary images (H1) | 4 |
| 2–3 | Cancel a rollout; a rolled-back digest isn't retried | Canary images in the throwaway registry | 3, 4 |
| 4 | Release notes as plain text, links only into the repository | Recorded GitHub releases fixtures, including Authelia's `/tree/<ref>` label | 1 |
| 5 | PVE apt rows with `Sys.Modify`; Unraid "can't check" | Fixtures for a token with and without the grant | 2 |
| 6 | PBS Verify now and its result | Recorded PBS exchanges for the verify task and its status | 2 |
| 7 | Coverage after a guest joins a job | The simulator changes a job; the next sync's coverage row changes | 2 |
| 8 | Unraid flash backup status | A design question, not a test: decided from Unraid's API schema; if a scope can read it, fixtures for both | 1 |
| 9 | A PVE backup failure through the webhook within a minute | Recorded PVE webhook payloads replayed with the instance's secret (H3) | 4 |
| 10 | A week of timeline on a resource | Test clock and simulator: a week generated, then read through the API and the sheet | 4 |
| 11 | The morning digest at its time, quiet on a quiet day | **Done:** `TestDigestArrivesAtItsTimeOnTheHubClock` (test clock, ntfy capture, Web Push decrypted); `TestDigestSaysNothingOnAQuietDay` | 2 |
| 12 | Acknowledge stops notifications; the digest lists it apart | **Done:** `TestAcknowledgedStopsEscalating`, `TestDigestNamesAcknowledged` (already there) | 2 |
| 13 | An agent proposes, the owner approves with a passkey, the audit has both | MCP client (H5), virtual authenticator (H4) | 4 |
| 14 | The docs site builds and publishes | Already the Docs job; publishing is CI's | CI |
| 15 | The public demo's hourly reset | Test clock | 4 |
| 16 | The installer | H7 | CI |
| 17 | The LXC script | H7 shim | CI |
| 18 | Specs against live instances | `spec test` against recorded answers (layer 2); `live:verify` for drift (layer 5) | 2, 5 |
| 19 | A community spec's actions stay off until turned on | A fixture community repository served by `httptest` | 2 |

14b's "Done" items stay as history. Its status becomes "superseded by 17".

## Agent tooling

Kept local, like `AGENTS.md`:
- **`AGENTS.md`** gets the rule, the layers and their commands, replacing "tests and typechecks are fine".
- **A local skill, `.claude/skills/test-first/`**, excluded in `.git/info/exclude`: which layer for which change, the harness pieces and how to use them, and the loop in H8.
- Public, for contributors: **CONTRIBUTING.md** gets a "Testing" section with the layers and commands, once H1 and H2 land.

## Changes to the other plans

- **House rule** in [15](15-v0.5.md) and [16](16-stacks-parity.md): tests run without asking, on throwaway things. The lab is read only by `live:verify`, never written by a test.
- **15, wave 32:** "the open v0.4 manual tests" becomes "H1–H4 and the 14b replacements".
- **16:** each wave ships its layer 3 and 4 tests; "Done when" names them; the lab cutover is an operation, not a test, and its result is checked by a script.

---

## Open questions T1–T4

| | Question | Recommendation |
|---|---|---|
| **T1** | May the agent run engine tests (layer 3) on the maintainer's machine without asking? They start throwaway containers and a registry, and remove them | **Yes**, as tests, not builds: they never run `docker build`, `bun run build` or a dev server. Everything carries the run's label and is removed. Layer 4 stays in CI, because it needs the release build |
| **T2** | May the agent run `live:verify` (read-only requests to the lab) without asking each time? | **Yes, read-only, standing.** It sends only GETs and Unraid queries, as today, and never wakes disks. Anything that writes still needs a go-ahead, and no test writes |
| **T3** | Is the official MCP TypeScript SDK fine as a test-only dependency? | **Yes.** The rule against third-party JavaScript covers the app. The SDK runs only in tests, isn't bundled, and is pinned like any other |
| **T4** | Does test automation mode get the test clock, or a separate flag? | **A separate flag, only allowed with test automation on.** Both show the warning banner and the security finding |

---

## Decisions

Settled by the maintainer on 2026-11-05: **T1–T4 all as recommended.**

1. **T1:** the agent runs engine tests on the maintainer's machine without asking. They never build, and remove everything they start.
2. **T2:** `live:verify` has a standing go-ahead, read-only. Writing to the lab still needs a go-ahead per run.
3. **T3:** the official MCP TypeScript SDK is fine as a test-only dependency, never bundled.
4. **T4:** the test clock has its own flag, allowed only with test automation on.

---

## Waves

| Wave | What |
|---|---|
| 32 | H1 engine tests and canary images; H2 test clock; H3 notification and Web Push fakes; H4 virtual passkeys; the 14b replacements for 1–4, 9–12, 15; the rule in `AGENTS.md`, the skill, CONTRIBUTING |
| 32a–32c | Stacks parity ([16](16-stacks-parity.md)), each verb tested at layer 3, each flow at layer 4 |
| 33 | H5 MCP client (13); H6 WebKit and PWA; H7 installer, LXC shim, upgrades; the rest of 14b; `live:verify` from the spike |

## Done when

- No plan, PR or commit after this one asks a person to check anything.
- Every open 14b item is a named test in CI, or a design decision recorded in its plan (8).
- `bun run test:engine` passes on a clean CI runner and leaves nothing behind (a final step lists containers, networks and volumes with the test label and fails if any remain).
- `bun run e2e:system` runs a rollout through a maintenance window, a broken image's rollback and an agent's proposal approved with a passkey, with no person and no lab.
- The nightly stress job runs layers 3 and 4 too.
