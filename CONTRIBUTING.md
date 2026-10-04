# Contributing to Cha Dash

Thanks for your interest! Cha Dash is building its **M0 foundations**: the hub, the layout engine, the design tokens and the Demo Home simulator are in place, and the web app is on its way. The product plan lives in [`docs/plan/`](docs/plan/), and decisions are recorded in [`docs/adr/`](docs/adr/README.md).

## How to help right now

- **Read the plan and push back.** Start with [docs/plan/01-vision.md](docs/plan/01-vision.md). Open an issue if something is wrong, missing, or could be better.
- **Share your homelab.** Which platforms, services and pain points should Cha Dash handle? Real setups shape what gets built first.
- **Design feedback.** Visual and interaction design is a core part of this project. Critique and ideas are very welcome.

## Code contributions

We'll start accepting code once the M0 foundations land (see [docs/plan/04-roadmap.md](docs/plan/04-roadmap.md)).

**Contributor License Agreement.** Before your first pull request can be merged, you sign our [CLA](CLA.md) by posting a one-line comment; the CLA Assistant bot will prompt you.
- You keep your copyright.
- Your contribution can only ever be released under **OSI-approved open-source licences**, never proprietary ones.
- The CLA lets the project change licence in future (for example, to a more permissive licence for institutions) without tracking down every contributor.

When code contributions open, expect a few house rules:

- **Integrations ship with recorded fixtures and contract tests** ([architecture → integration framework](docs/plan/03-architecture.md#integration-framework)).
- **Every widget is designed for all its sizes and states** (loading, live, stale, error, empty) ([experience doc](docs/plan/02-experience.md#widget-anatomy-and-states)).
- **No third-party JavaScript in the app.** Integrations emit data, and our widgets render it.

## Developing

The hub is one Go binary with the Vue SPA embedded. JavaScript is development tooling only; nothing from it ships except the built SPA.

**Prerequisites**
- **Go.** `go.mod` pins Go 1.27.1. Any Go from 1.21 on downloads that toolchain by itself the first time you run a `go` command.
- **Bun 1.4 or newer.** It is the package manager, workspace manager, script runner and test runner ([ADR-0019](docs/adr/0019-bun-for-javascript-tooling.md)). You don't need Node or pnpm.
- **Docker**, only to build the image.

**Install, test and typecheck**

```sh
bun install          # every JavaScript workspace: web/, packages/*, design/tokens
bun run typecheck
bun run test
go test ./...        # needs no SPA build
```

After editing a theme in `design/tokens/themes/`, run `bun run generate` in `design/tokens`. CI fails when `dist/` is stale.

**Running locally.** One command. The hub runs on the Demo Home simulator, so you need no homelab:

```sh
bun run dev          # hub on 127.0.0.1:3696 (data in ./data) + the SPA with hot reload on :5173
```

It builds the hub, waits until it's healthy, and starts the Vite dev server (which proxies `/api` and `/ws` to the hub). Open http://localhost:5173 and press **Login as Local Dev**: no setup token, no password. Ctrl-C stops both.

- **What the button does.** It signs in as the owner. On first run there is no owner, so it creates a passwordless one named `dev`. To try the real first-run flow, use the setup token the script prints.
- **Where it exists.** Only `bun run dev` builds the hub with `-tags devlogin`, and only the Vite dev server shows the button (`web/src/dev/`).
- **Safety.** The route answers loopback clients only. Release builds and images contain neither half, and CI checks the image's build tags, that the route answers 404, and that the bundle has no `web/src/dev/` code.

| Command | What it does |
|---|---|
| `bun run dev:fresh` | Wipes `./data` first: a brand-new instance, no owner yet |
| `bun run dev --now` | Runs the demo on the wall clock instead of from 14:05 |
| `bun run dev --check` | Checks prerequisites and ports, starts nothing |

`CHADASH_*` variables pass through to the hub (e.g. `CHADASH_DEMO_SEED=2 bun run dev`). The manual way still works: `CHADASH_DATA_DIR=./data go run ./cmd/chadash serve --demo` in one terminal and `cd web && bun run dev` in another.

**Building**

```sh
bun run build        # the SPA, into internal/webui/dist/
CGO_ENABLED=0 go build -tags webui -trimpath -o chadash ./cmd/chadash
docker build -t chadash:dev .        # the image, from the same steps
```

**`-tags webui` embeds the SPA** from `internal/webui/dist/`, so build the SPA first. Without the tag the hub serves a placeholder page, which is all `go test` and `go run` need.

**CI** ([`.github/workflows/ci.yml`](.github/workflows/ci.yml)) runs the same steps on every pull request:

| Job | Checks |
|---|---|
| Go | gofmt, `go vet`, `go test -race` (and again with `-tags devlogin`), staticcheck, govulncheck |
| JavaScript | frozen `bun install`, typecheck, tests, design tokens up to date |
| Build | the SPA, **initial JS ≤ 150 kB gzipped**, the hub with `-tags webui` |
| Image | amd64 and arm64 builds (no push), a smoke run, **image ≤ 40 MB compressed** |

The budgets come from [03 · Architecture](docs/plan/03-architecture.md#performance-budgets-enforced-in-ci-where-possible). Raising one takes a reason in the pull request.

## Fixtures and privacy

Fixtures must **never** contain real secrets, domains, public IPs or personal names. Sanitize them the way [testdata/importers/homepage/dogfood](testdata/importers/homepage/dogfood/README.md) does:

- replace domains with reserved example names (`*.example`)
- replace secrets with fake values in the same format
- map LAN IPs to a different private range

## Security

Please report vulnerabilities privately. See [SECURITY.md](SECURITY.md).

## License

Cha Dash is licensed under the [GNU Affero General Public License v3.0 or later](LICENSE). Contributions are accepted under the [CLA](CLA.md).
