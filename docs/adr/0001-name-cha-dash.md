# ADR-0001 · Name: Cha Dash

**Status:** accepted · **Date:** 2026-10-03 · **Roadmap:** [decision 1](../plan/04-roadmap.md#decisions)

## Context

The name shows up everywhere at once: the repo, two binaries, two images, Docker labels, environment variables, the compose metadata key, the domain and the Go module path. Changing it later breaks users' compose files and catalog listings, so it has to be settled before the first release.

## Decision

**The product is Cha Dash**, at `dash.cha.sh`.

| Where | Name |
|---|---|
| Hub binary and image | `chadash` |
| Node binary and image | `chadash-node` |
| Docker labels | `chadash.*` |
| Compose metadata | `x-chadash:` |
| Environment variables | `CHADASH_*` (e.g. `CHADASH_SECRET_KEY_FILE`, `CHADASH_HUB`) |
| Repository | `cha-dash` ([ADR-0008](0008-repo-and-images.md)) |
| Go module | `dash.cha.sh` ([ADR-0018](0018-go-module-path.md)) |

## Consequences

- One lowercase slug, `chadash`, works as a binary name, a label namespace and (uppercased) an env prefix, with no quoting or escaping.
- In the UI, "node" always means `chadash-node`. Proxmox machines are called **hosts**, so the two never get confused.
- Once images, labels and catalog listings exist, the name is effectively permanent.

## Alternatives considered

- **The hyphenated `cha-dash` everywhere.** It matches the repo, but hyphens aren't valid in environment variable names and read awkwardly as a label prefix.
- **A working name until launch.** It defers the choice, but every artifact made in the meantime would need renaming.
