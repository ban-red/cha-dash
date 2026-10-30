# TypeScript 7.1 typecheck (`@chadash/typecheck`)

A second, non-blocking typecheck of `web/` and `packages/layout-vue` with TypeScript 7.1 and Vue's content mapper (`@vue/content-mapper`, Vue Language Tools v4 alpha), next to `vue-tsc`, which stays the gate.

```sh
bun run typecheck:next   # from the repo root
```

**Why a workspace of its own.** TypeScript 7 is native and has no JavaScript API, and two things need one package named `typescript`:

- `vue` calls `require('typescript')` to resolve imported prop types when Vite or Vitest compile a component. That has to be TypeScript 6, so `web/` and `packages/layout-vue` keep it.
- The content mapper loads `typescript/ast` from the directory of the tsconfig it checks. That has to be TypeScript 7.1.

So the tsconfigs here (`web.json`, `layout-vue.json`) extend each workspace's own and add `contentMappers`, and this folder's `typescript` is 7.1. `web.json` clears `types`: `web/env.d.ts` already references `vite/client` from inside `web/`.

**What it needs.** Node.js 20.19 or 22.12 and later: TypeScript runs the mapper as a Node process (`--runExternalCode`). Bun stays the only tool everything else needs ([ADR-0019](../../docs/adr/0019-bun-for-javascript-tooling.md)).

**Why it doesn't block.** The mapper reports a false TS2367 ("This comparison appears to be unintentional") for any event handler inside a `v-else-if` branch, [vuejs/language-tools#6306](https://github.com/vuejs/language-tools/issues/6306): 26 of them today. Once that's fixed it should report nothing; then it can replace `vue-tsc`. The Vue options it reads are the mapper entry's `options`, not `vueCompilerOptions`, and v4 dropped `strictTemplates` (template checks follow the script's strictness).

**The alphas.** `@vue/content-mapper` 4.0.0-alpha.2 pins three alphas published the same day; `bunfig.toml` exempts all four from the three-day wait until 2026-11-02.
