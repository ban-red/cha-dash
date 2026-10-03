# ADR-0019 · Bun for JavaScript tooling

**Status:** accepted · **Date:** 2026-10-03 · **Next steps:** [Track 3.1](../plan/06-next-steps.md#track-3-hub-skeleton-and-the-demo-home-simulator)

## Context

Two JavaScript workspaces are on the way: `web/` (the Vue SPA, [ADR-0003](0003-frontend-vue-and-vite.md)) and `packages/layout` (the layout engine, with property tests). The plan said pnpm, which still needs Node and a separate test runner. Bun covers package management, workspaces and tests in one fast tool, and Bun 1.4 is already installed on the maintainer's machine. JavaScript is development tooling only: the shipped product is a Go binary with the built SPA embedded.

## Decision

**Bun replaces pnpm** as the package manager, the workspace manager and the script runner, and **`bun test`** runs the tests of plain TypeScript packages. **Vite stays** the dev server and bundler.

## Consequences

- **Lockfile:** `bun.lock` (text), committed. No `package-lock.json`, `pnpm-lock.yaml` or `yarn.lock`.
- **Workspaces** are declared in the root `package.json`.
- **Plain TypeScript packages** (the layout engine, the token generator) test with `bun test`, including the fast-check property tests.
- **Vue component tests keep Vitest**, launched through Bun scripts. Bun's test runner can't compile `.vue` files without an extra plugin, while Vitest reuses Vite's Vue transform. Revisit once `web/` exists. Playwright stays for end-to-end and visual regression tests.
- **CI installs a pinned Bun version** and runs lint, typecheck and tests with it ([Track 3.10](../plan/06-next-steps.md#track-3-hub-skeleton-and-the-demo-home-simulator)).
- Contributors install Bun instead of Node plus pnpm. The contributor guide should say so once code lands.
- Bun never ships, so the release image and the footprint promises are unaffected.
- If one tool misbehaves under Bun, the fallback is to run that tool under Node, not to switch everything back.

## Alternatives considered

- **pnpm on Node** (the earlier plan). Mature and strict, but it means installing Node, pnpm and a separate test runner.
- **npm.** Ships with Node, but it's slower and still needs a separate test runner.
- **Bun as the bundler too.** Possible, but the SPA relies on Vite's plugins (Vue, PWA).
