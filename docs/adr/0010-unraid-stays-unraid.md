# ADR-0010 · Unraid stays Unraid

**Status:** accepted · **Date:** 2026-10-03 · **Roadmap:** [decision 10](../plan/04-roadmap.md#decisions)

## Context

Unraid is a first-class platform, and it's the main app host in the dogfood lab (jupiter). Its containers are created and updated by Unraid's own Docker UI from templates, not from compose files. Unraid has an official GraphQL API, with a consent flow for issuing API keys.

## Decision

**Unraid-managed containers stay Unraid-managed.**
- Cha Dash reads and controls them **through the Unraid API**. One-click connect uses its `ApiKeyAuthorize` consent flow.
- **Nothing is converted to compose.** Owned stacks ([ADR-0006](0006-owned-stacks-and-thin-node.md)) target compose hosts such as pvedocker.
- **An optional thin node on Unraid may come later** (v0.3+, as a Community Apps container). It adds what the API lacks: host metrics, per-container stats, logs, opt-in exec, SMART detail and image-update checks. Lifecycle still goes through the Unraid API, and the node never takes ownership of Unraid's containers.

## Consequences

- Unraid's own UI keeps working, and Unraid stays the source of truth for its containers.
- There are two control paths for containers: the node on compose hosts, and the Unraid API on Unraid. The Home Graph merges both views of the same container.
- Control on Unraid (Docker and VM power, container updates, parity, array) arrives in v0.2 through the API, with no node required.
- Owned stacks, with their git history and rollback, don't apply to Unraid-managed containers. That's deliberate.

## Alternatives considered

- **Convert Unraid containers to compose stacks.** One model everywhere, but it breaks Unraid's UI and template updates, and asks users to trust a migration.
- **Drive Unraid's Docker directly through a node.** Fuller control, but two tools would write the same containers, with no single source of truth.
- **Read-only Unraid.** Safe, but it fails the Unraid owner who wants to act from their phone.
