# Contributing to Cha Dash

Thanks for your interest! Cha Dash is in beta: 0.1, the read-only Front Page, is out, and 0.2, Control, is nearly done. The product plan lives in [`docs/plan/`](docs/plan/), with the latest review in [09 · Roadmap review](docs/plan/09-roadmap-2026.md), and decisions are recorded in [`docs/adr/`](docs/adr/README.md).

## How to help right now

- **Read the plan and push back.** Start with [docs/plan/01-vision.md](docs/plan/01-vision.md). Open an issue if something is wrong, missing, or could be better.
- **Share your homelab.** Which platforms, services and pain points should Cha Dash handle? Real setups shape what gets built first.
- **Design feedback.** Visual and interaction design is a core part of this project. Critique and ideas are very welcome.

## Code contributions

The M0 foundations have landed, so code contributions are open. For anything bigger than a fix, open an issue first so we can agree on the shape.

**Contributor License Agreement.** Before your first pull request can be merged, you sign our [CLA](CLA.md) by posting a one-line comment; the CLA Assistant bot will prompt you.
- You keep your copyright.
- Your contribution can only ever be released under **OSI-approved open-source licences**, never proprietary ones.
- The CLA lets the project change licence in future (for example, to a more permissive licence for institutions) without tracking down every contributor.

A few house rules:

- **Integrations ship with recorded fixtures and contract tests** ([architecture → integration framework](docs/plan/03-architecture.md#integration-framework)).
- **Every widget is designed for all its sizes and states** (loading, live, stale, error, empty) ([experience doc](docs/plan/02-experience.md#widget-anatomy-and-states)).
- **No third-party JavaScript in the app.** Integrations emit data, and our widgets render it.

## Developing

The hub is one Go binary with the Vue SPA embedded. The node (`cmd/chadash-node`) is a second binary and image, built from the same module. JavaScript is development tooling only; nothing from it ships except the built SPA.

**Prerequisites**
- **Go.** `go.mod` pins Go 1.27.3. Any Go from 1.21 on downloads that toolchain by itself the first time you run a `go` command.
- **Bun 1.4 or newer.** It is the package manager, workspace manager, script runner and test runner ([ADR-0019](docs/adr/0019-bun-for-javascript-tooling.md)). You don't need Node or pnpm.
- **Docker**, only to build the image.

**Install, test and typecheck**

```sh
bun install          # every JavaScript workspace: web/, packages/*, design/tokens
bun run typecheck
bun run test
bun run lint         # oxlint, then ESLint for Vue templates
go test ./...        # needs no SPA build
go tool -modfile=tools/go.mod golangci-lint run      # the Go linters, pinned in tools/go.mod
go fix ./...         # CI fails if the Go 1.27 modernizers would change anything
```

After editing a theme in `design/tokens/themes/`, run `bun run generate` in `design/tokens`. CI fails when `dist/` is stale.

**End-to-end tests** ([`web/e2e/`](web/e2e/)) drive the release binary in a real browser on the Demo Home: the first run, the front page on desktop and phone, axe in every theme, the command bar, and the safety ladder on the action engine. Playwright's test runner needs Node.js (any current LTS); nothing else here does.

```sh
bun run build && CGO_ENABLED=0 go build -tags webui -o chadash ./cmd/chadash   # the binary they test
(cd web && bunx playwright install chromium)                                  # once
bun run e2e          # starts the hub on 127.0.0.1:3698 with a fresh data directory
```

`CHADASH_E2E_BIN` tests another binary, `CHADASH_E2E_PORT` moves the hub, and `CHADASH_E2E_CHROMIUM` points at a Chromium of your own where Playwright can't install one.

**Running locally.** One command. The hub runs on the Demo Home simulator, so you need no homelab:

```sh
bun run dev          # hub on 127.0.0.1:3696 (data in ./data) + the SPA with hot reload on :3697
```

It builds the hub, waits until it's healthy, and starts the Vite dev server (which proxies `/api` and `/ws` to the hub). Open http://localhost:3697 and press **Login as Local Dev**: no setup token, no password. Ctrl-C stops both.

- **What the button does.** It signs in as the owner. On first run there is no owner, so it creates a passwordless one named `dev`. To try the real first-run flow, use the setup token the script prints.
- **Where it exists.** Only `bun run dev` builds the hub with `-tags devlogin`, and only the Vite dev server shows the button (`web/src/dev/`).
- **Safety.** The route answers loopback clients only. Release builds and images contain neither half, and CI checks the image's build tags, that the route answers 404, and that the bundle has no `web/src/dev/` code.

| Command | What it does |
|---|---|
| `bun run dev:fresh` | Wipes `./data` first: a brand-new instance, no owner yet |
| `bun run dev --now` | Runs the demo on the real clock in UTC, with the script at its own hours, instead of from 14:05 shown as your local time |
| `bun run dev --check` | Checks prerequisites and ports, starts nothing |

`CHADASH_*` variables pass through to the hub (e.g. `CHADASH_DEMO_SEED=2 bun run dev`). The manual way still works: `CHADASH_DATA_DIR=./data go run ./cmd/chadash serve --demo` in one terminal and `cd web && bun run dev` in another.

**Building**

```sh
bun run build        # the SPA, into internal/webui/dist/
CGO_ENABLED=0 go build -tags webui -trimpath -o chadash ./cmd/chadash
docker build -t chadash:dev .        # the image, from the same steps
CGO_ENABLED=0 go build -trimpath -o chadash-node ./cmd/chadash-node   # the node
docker build --target node -t chadash-node:dev .                      # its image
```

**The node links the Docker client** (`github.com/moby/moby/client`), and with stacks the Compose SDK and go-git. The hub must never: `cmd/chadash`'s `TestHubFootprint` fails if `go list -deps ./cmd/chadash` names any of them.

**`-tags webui` embeds the SPA** from `internal/webui/dist/`, so build the SPA first. Without the tag the hub serves a placeholder page, which is all `go test` and `go run` need.

**CI** ([`.github/workflows/ci.yml`](.github/workflows/ci.yml)) runs the same steps on every pull request:

| Job | Checks |
|---|---|
| Go | gofmt, `go vet`, `go test -race` (and again with `-tags devlogin`), an empty `go fix -diff`, golangci-lint ([`.golangci.yml`](.golangci.yml), staticcheck included), govulncheck; the Dockerfile's `hub` and `node-build` stages COPY every directory their binary imports |
| JavaScript | frozen `bun install`, typecheck, lint, tests, design tokens up to date |
| Workflows | zizmor's security audits of `.github/workflows/`; accepted findings and their reasons are in [`.github/zizmor.yml`](.github/zizmor.yml) |
| Build | the SPA, **initial JS ≤ 150 kB gzipped**, the hub with `-tags webui`, and chadash-node |
| End-to-end | the hub with the SPA embedded, in Chromium: first run, the front page on desktop and phone, **axe (WCAG 2.2 AA) in all six themes**, the command bar, a T1 action and a T2 confirmation |
| Image | amd64 and arm64 builds of the hub and the node (no push), a smoke run of each, **hub image ≤ 40 MB compressed**, **node binary ≤ 40 MB** |
| Footprint (non-blocking) | the hub's RSS budgets, and the node binary's size |

CodeQL ([`codeql.yml`](.github/workflows/codeql.yml): Go, TypeScript and the workflows) runs on every pull request too, and OpenSSF Scorecard ([`scorecard.yml`](.github/workflows/scorecard.yml)) runs on `main`.

**Dependencies** are updated by [Renovate](renovate.json5), in weekly grouped pull requests. A new release waits three days before Renovate proposes it, and `bun install` won't take it either ([`bunfig.toml`](bunfig.toml)). Go tools are `tool` directives in [`tools/go.mod`](tools/go.mod); run `go mod tidy` in that directory after changing them.

The budgets come from [03 · Architecture](docs/plan/03-architecture.md#performance-budgets-enforced-in-ci-where-possible). Raising one takes a reason in the pull request.

## Releasing

The maintainer cuts releases by pushing a version tag. [`.github/workflows/release.yml`](.github/workflows/release.yml) does the rest and stops at a **draft** GitHub release, so nothing is announced until someone has read it. It never runs on branches or pull requests.

| Job | What it does | Permissions |
|---|---|---|
| Prepare | Checks the tag (`vMAJOR.MINOR.PATCH`, optionally `-beta.N` or `-rc.N`), works out the image tags, and fails early if `CHANGELOG.md` has no section for the version | `contents: read` |
| Build and push | The binaries from the Dockerfile's `binary` stage (linux amd64, arm64 and armv7; darwin amd64 and arm64) and `node-binary` stage (linux only), checked and summed into `SHA256SUMS`; smoke-tested linux/amd64 hub and node images; then both pushed for linux/amd64, arm64 and arm/v7 to `ghcr.io/ban-red/chadash` and `ghcr.io/ban-red/chadash-node`, with provenance and SBOMs | `contents: read`, `packages: write` |
| Sign | cosign keyless signing of both images (by digest) and of `SHA256SUMS`, each verified straight after | `packages: write`, `id-token: write`, `actions: read` |
| Draft release | The draft, marked prerelease for `-beta` and `-rc`: notes from `CHANGELOG.md` plus install and verify instructions, with the binaries, `SHA256SUMS` and its signature attached | `contents: write`, `actions: read` |

The node image gets the same tags as the hub.

| Tag pushed | Image tags |
|---|---|
| `v0.1.0-beta.2` | `0.1.0-beta.2`, `beta` |
| `v0.1.0-rc.1` | `0.1.0-rc.1` |
| `v0.1.0` | `0.1.0`, `0.1`, and `latest` if it's the newest stable tag |

**Cutting a beta:**

1. **Start from `main` with CI green** on the commit you're going to tag.
2. **Check `COSIGN_VERSION`** at the top of `release.yml`. It should be the current cosign release; Renovate proposes new ones. A new major version changes the module path in the install step too (`github.com/sigstore/cosign/v3/...`).
3. **Write the changelog.** Move what's under `## [Unreleased]` in `CHANGELOG.md` into a new `## [0.1.0-beta.2] - YYYY-MM-DD` section, written for people who run Cha Dash. It becomes the release text, so links must be full URLs. Update the comparison links at the bottom. Preview it with `scripts/release-notes.sh 0.1.0-beta.2`.
4. **Merge that to `main`** the usual way.
5. **Tag the merge commit and push the tag:**
   ```sh
   git tag -a v0.1.0-beta.2 -m "Cha Dash 0.1.0-beta.2"
   git push origin v0.1.0-beta.2
   ```
6. **Watch the Release run** in Actions. Building and installing cosign take most of its time.
7. **Read the draft** under Releases: the notes, the image tags, the attached files. To check the signature yourself, run the `cosign verify` command in the notes. Then press **Publish**.

**If something goes wrong,** don't move or reuse a pushed tag: images may already be published under it. Fix it on `main` and tag the next beta. Re-running the workflow for the same tag is safe; it rebuilds and pushes the same image tags again, and updates the draft instead of adding a second one.

**The first release only** (and the first with the node, for `chadash-node`): ghcr.io makes a new package private. Open the package's settings, make it public, and check it's linked to this repository. If the package already existed, give the repository write access under *Manage Actions access*, or the push is refused.

**At 0.1.0,** the first stable release: point the Unraid template at `latest` and drop `Status:Beta` from its category ([deploy/unraid](deploy/unraid/README.md#for-the-maintainer-listing-it-in-ca)), take the `beta` override out of [docs/beta.md](docs/beta.md) and [deploy/README.md](deploy/README.md), and fill in *Supported versions* in [SECURITY.md](SECURITY.md).

## Fixtures and privacy

Fixtures must **never** contain real secrets, domains, public IPs or personal names. Sanitize them the way [testdata/importers/homepage/dogfood](testdata/importers/homepage/dogfood/README.md) does:

- replace domains with reserved example names (`*.example`)
- replace secrets with fake values in the same format
- map LAN IPs to a different private range

## Security

Please report vulnerabilities privately. See [SECURITY.md](SECURITY.md).

## License

Cha Dash is licensed under the [GNU Affero General Public License v3.0 or later](LICENSE). Contributions are accepted under the [CLA](CLA.md).
