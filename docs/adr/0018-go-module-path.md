# ADR-0018 · Go module path: dash.cha.sh

**Status:** accepted · **Date:** 2026-10-03 · **Roadmap:** [decision 18](../plan/04-roadmap.md#decisions)

## Context

A Go module path is close to permanent: every import line and every `go install` command carries it. The default choice ties the path to where the code is hosted, and to the hyphenated repo slug.

## Decision

**The module path is `dash.cha.sh`** (`module dash.cha.sh` in `go.mod`), so the hub installs with:

```bash
go install dash.cha.sh/cmd/chadash@latest
```

`dash.cha.sh` answers every path with `go-import` (and `go-source`) meta tags when `?go-get=1` is present, pointing at the GitHub repo. [docs/infra/vanity-import.md](../infra/vanity-import.md) has two ways to serve them.

## Consequences

- Import paths match the product's name and domain, and don't change if the code ever moves.
- Local builds work today. The tags only matter for `go install` from outside the repo and for pkg.go.dev. Serving them is still to do (Track 0.5).
- The domain has to keep answering. The Go module proxy caches versions it has fetched, but each new version is first fetched from the origin.
- Throwaway code gets its own module under the same path, kept out of the main module (e.g. `dash.cha.sh/spikes/node-compose`).

## Alternatives considered

- **`github.com/ban-red/cha-dash`.** Works with no setup, but ties every import to the host and the hyphenated slug.
