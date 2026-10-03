# ADR-0012 · Visual direction: Instrument, built to be themeable

**Status:** accepted · **Date:** 2026-10-03 · **Roadmap:** [decision 12](../plan/04-roadmap.md#decisions)

## Context

[Prototype v1](../../design/prototypes/README.md) rendered the dogfood home in two directions. **A · Instrument** is warm graphite, hairlines, tabular numerals and one amber signal color. **B · Paper** is a literal newspaper front page: a serif headline, column rules, newsprint. The plan was to pick one or blend them. People also want their dashboard to feel like theirs, and a wall at night needs a different look from a desk at noon. See [02 · Experience → Theming](../plan/02-experience.md#theming).

## Decision

**Direction A, built to be very themeable.**
- **A's structure is the product:** the unit grid, sections, widget anatomy, status vocabulary, interaction grammar and motion physics.
- **Everything visual is a theme token.** A theme is a versioned token document (YAML/JSON) that the hub validates and compiles to CSS custom properties. Components only use semantic tokens. Switching themes is instant.
- Themes cover color (12-step OKLCH scales generated from base, accent and contrast), status colors, typography, surface (`card`, `ruled`, `flat`), shape, density, Briefing style, material, motion and backdrop.
- **Status colors can be overridden, but must pass the palette validator.** A failing theme is refused with a specific reason.
- **Direction B survives as the built-in Paper theme**, with ruled surfaces, the newsprint palette and serif headlines, plus an optional `headline` style for the Briefing.
- Built-in themes: **Instrument Dark** (default), Instrument Light, OLED, Night, High Contrast and Paper. Themes are chosen per board and per device.

## Consequences

- Prototype v2 demos the theme engine. Tokens v0 and the theme schema follow (Track 1).
- No raw colors or sizes in components. Reviews check for semantic tokens.
- The Workbench and visual regression multiply by theme, so the built-in set stays small.
- Imported themes are validated before they apply. Custom CSS is an admin-only escape hatch with no stability promise, and the CSP still stops it from loading anything from elsewhere.
- Accessibility holds across themes because contrast and status separation are checked, not hoped for.

## Alternatives considered

- **B · Paper as the product.** Distinctive, but lower density, and harder to make dark mode feel intentional.
- **A blend** (A's structure with B's light theme). A plus a Paper theme covers it better.
- **C · Glass** (depth, materials, vibrant backdrops). Legibility problems, and content over a busy backdrop works against calm design.
- **One fixed look.** Less to build and test, but it can't adapt to a wall at night, an OLED kiosk or personal taste.
