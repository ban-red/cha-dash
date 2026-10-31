# ADR-0025 · The spec format public as `v1alpha1` before 1.0

**Status:** accepted · **Date:** 2026-10-31 · **Roadmap:** [09 · D3](../plan/09-roadmap-2026.md#7-decisions-this-needs)

## Context

Tier 1 integrations are YAML specs the hub's engine runs (`internal/spec`). Nine ship in the hub, in `chadash/v1alpha1`. Until now the format was internal: documented only as an annotated sample, and free to change with any release. A community wants to write specs for the services Cha Dash doesn't cover, and to share them. They can't build on a format that may change under them without notice.

## Decision

**The spec format is public from v0.3 as `chadash/v1alpha1`, with a published JSON Schema, and it changes only by a new `apiVersion`.**

- The schema is generated from the engine's Go types (`spec.Schema()`), so the two can't drift. Every hub serves it at `/api/specs/schema/v1alpha1.json`, and the reference is [`docs/specs.md`](../specs.md).
- "Alpha" means the format may still change. A change ships a new `apiVersion` and a migration from the old one, and the engine loads both versions for at least one minor release.
- Authors check a spec without a hub: `chadash spec lint` (the schema and the load-time rules) and `chadash spec test` (a run against recorded answers).
- The owner installs a spec from Settings. It's kept in the database (`installed_specs`), so backups and exports carry it. Its reads start at once; its actions stay off until the owner turns them on after seeing each one.
- The community repository, `ban-red/cha-dash-specs`, takes specs with recorded fixtures, runs lint and test in CI, and has a maintainer review every pull request. The hub doesn't fetch from it in v0.3.

## Consequences

- The engine's types are now an API. Renaming a key or changing an expression's meaning needs a new `apiVersion` and a migration, which slows changes to the engine.
- An installed spec is code paths the owner chose to trust. It's bounded the same way a bundled spec is: requests only under the instance's URL, secrets only where the spec names them, sandboxed expressions, at most 16 actions with declared tiers. Actions that claim one thing and do another are the risk left, hence actions off by default.
- `v1` waits until the format has held still through real community specs.

## Alternatives considered

- **Keep it internal until 1.0.** No community specs until then, and every missing service waits for the maintainer.
- **Publish it as `v1` now.** It would freeze choices made before anyone outside used the format.
- **Install specs from a folder on disk.** Doesn't survive a container recreate without another volume, and isn't in backups.
