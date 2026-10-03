# ADR-0003 · Frontend: Vue 3.6 and Vite

**Status:** accepted · **Date:** 2026-10-03 · **Roadmap:** [decision 3](../plan/04-roadmap.md#decisions)

## Context

The UI is most of what people judge Cha Dash on: spring physics, layout animations, a live graph fed by WebSocket diffs, and phone and wall layouts. It ships inside the Go hub binary and has to run under a strict CSP (no inline scripts, no `unsafe-eval`). It also needs a large pool of potential contributors. See [03 · Architecture → Tech stack](../plan/03-architecture.md#tech-stack).

## Decision

- **Vue 3.6** (RC now; stable once released) with **the latest Vite**.
- **A plain SPA**, with no server-side rendering and no meta-framework. The build is embedded in the hub with `go:embed`.
- `<script setup lang="ts">` and the Composition API throughout. Templates are precompiled, so only the runtime build ships.
- **Vapor mode is opt-in per component**, and only where a measurement shows a gain. Candidates are hot widget internals: rolling numerals, sparklines, grid cells.
- Companions: Pinia, Vue Router, Reka UI (headless primitives styled only with our tokens), Motion for Vue and VueUse.

## Consequences

- Motion for Vue gives us interruptible springs and layout animations, which "expand in place" and section reflow depend on.
- Precompiled templates keep the CSP strict.
- During the RC, versions are pinned and standard (non-Vapor) components are the default. The layout engine is a framework-agnostic TypeScript package, so framework churn can't trap it.
- The graph store holds large data in `shallowRef` / `markRaw` to avoid deep reactivity.
- No SSR is needed for a fast first paint: the hub inlines the last-known snapshot into `index.html`.
- JavaScript tooling around Vite is Bun ([ADR-0019](0019-bun-for-javascript-tooling.md)).

## Alternatives considered

- **Svelte 5.** The earlier recommendation. Small output, but nothing equivalent to Motion's layout animations, and a smaller contributor pool.
- **React 19.** Also a large contributor pool. Vue was the maintainer's choice.
- **An SSR meta-framework.** Adds a JavaScript server runtime next to the Go binary, for pages that are private and live anyway.
