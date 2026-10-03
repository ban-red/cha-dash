# ADR-0002 · Licence: AGPL-3.0 with an OSI-limited CLA

**Status:** accepted · **Date:** 2026-10-03 · **Roadmap:** [decision 2](../plan/04-roadmap.md#decisions)

## Context

Cha Dash is software people run on their own network and sometimes share with others. We want three things:
- improvements to the hub and node stay open, including when someone runs a modified copy as a service;
- contributors can trust that their work stays open source;
- room to change the licence later (for example, a more permissive one for institutions, or dual licensing) without tracking down every contributor.

## Decision

- **The hub and node are AGPL-3.0-or-later.** See [LICENSE](../../LICENSE).
- **The spec schema, design tokens and icon pipeline are Apache-2.0**, so integration authors, theme makers and other tools can reuse them freely.
- **Contributions come in under an [OSI-limited CLA](../../CLA.md)**, signed with a one-line pull request comment. The CLA Assistant bot records it on the `cla-signatures` branch.
  - Contributors keep their copyright and can use their own work however they like.
  - The project gets a broad licence to the contribution, including the right to relicense.
  - **Relicensing is limited to OSI-approved licences.** Contributions can never be distributed under proprietary terms, and that limit binds any successor steward.

## Consequences

- Anyone who runs a modified hub or node for others over a network has to offer them the source.
- First-time contributors sign once, before their first pull request is merged. The bot explains how.
- A move to a more permissive licence stays possible. A closed-source version never is.
- Every component that isn't AGPL states its licence clearly, so the boundary stays obvious.
- Some employers don't let staff contribute to AGPL projects or sign CLAs. We accept that.

## Alternatives considered

- **A permissive licence for everything.** The easiest to adopt, but a modified hosted copy could close its source.
- **GPL-3.0.** Its source obligation doesn't cover software used over a network, which is how a dashboard is used.
- **A sign-off (DCO) with no CLA.** Lighter for contributors, but any future relicensing would need every contributor's consent.
- **A standard CLA or copyright assignment.** Allows proprietary relicensing, which costs contributor trust. Assignment also takes away their copyright.
