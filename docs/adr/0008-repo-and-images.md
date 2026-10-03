# ADR-0008 · Repository and image names

**Status:** accepted · **Date:** 2026-10-03 · **Roadmap:** [decision 8](../plan/04-roadmap.md#decisions)

## Context

Distribution channels remember names. Unraid's Community Apps catalog can blacklist a repository that gets renamed after submission, and image references end up in many people's compose files. The names have to be settled before the first listing.

## Decision

| What | Name |
|---|---|
| Repository | **[github.com/ban-red/cha-dash](https://github.com/ban-red/cha-dash)**, public |
| Hub image | `ghcr.io/ban-red/chadash` |
| Node image | `ghcr.io/ban-red/chadash-node` |

The repo was renamed to lowercase on 2026-10-03, **before** any catalog submission. **No more renames or transfers once Cha Dash is submitted to Community Apps.**

## Consequences

- GitHub redirects the old mixed-case URL, so links already shared keep working.
- Images live next to the code on GHCR, built and signed by the repo's own CI.
- The repo slug is hyphenated (`cha-dash`); the binary and image slugs aren't (`chadash`, [ADR-0001](0001-name-cha-dash.md)).
- The Go module path doesn't depend on where the repo lives ([ADR-0018](0018-go-module-path.md)).
- Once the catalog listing exists, moving the repo to another account or organization is off the table.

## Alternatives considered

- **Docker Hub for images.** Familiar, but a second account with its own permissions, and pull rate limits for anonymous users.
- **Keep the mixed-case repo name.** It works, but lowercase matches the image names and avoids case surprises in tools.
- **A GitHub organization first.** More overhead than a one-maintainer project needs today.
