# ADR-0032 · UI primitives live in `web/src/ui`

**Status:** accepted · **Date:** 2026-10-30

## Context

Design tokens already have one source: theme documents in [design/tokens](../../design/tokens/README.md), compiled to `tokens.css`. Components did not. Buttons were a CSS class written by hand on about 230 elements, the close button's SVG was pasted twelve times, and two dialog shells (`act-dlg`, `edit-dlg`) were near copies. Copies drifted:

- `.btn.danger` was defined three times, one of them in Settings' stylesheet. The board's "Remove section" button only turned red after Settings had been opened, and the tile menu used a different red.
- Select and textarea styles lived in Settings too, so fields elsewhere depended on Settings having loaded.
- Many fields showed help text that wasn't tied to the input with `aria-describedby`.

## Decision

**Every reusable visual building block is a component in `web/src/ui/`. Feature code composes them and doesn't restyle them.**

- **Layers:** tokens (`@chadash/tokens`) → primitives (`ui/`) → features (`components/`, `settings/`, `board/`, `widgets/`, …). A primitive uses semantic tokens only. A feature uses primitives and tokens, and keeps layout CSS of its own.
- **First set:** `UiButton`, `UiCloseButton`, `UiField`, `UiDialog`, and the marks (`Glyph`, `Meter`, `MiniGraph`), moved from `components/marks`.
- **Imported by path** (`../ui/Button.vue`), not through a barrel file. `UiDialog` imports its CSS, and a barrel would pull that side effect into the first-paint chunk.
- **Styles stay global and load early.** `ui/ui.css` loads in `main.ts` straight after `base.css`, so the cascade order every feature override relies on doesn't change. `UiDialog` imports `ui/dialog.css`, which loads with the first dialog chunk.
- **The DOM keeps its class names** (`.btn.primary`, `.x`, `.field`). Tests, the e2e suite and layout CSS that targets them keep working.
- **A test enforces the boundary.** `test/ui-boundary.test.ts` fails when feature code writes `class="btn"`, `class="x"`, `class="field"` or a `<dialog>` by hand.
- **A catalog shows every primitive in every variant**, at `/dev/ui` on the Vite dev server only. `vite build` drops it.

## Consequences

- One place to change a button or a dialog, and one place to review it for WCAG 2.2 AA. The axe tests cover the catalog's states.
- New primitives go in `ui/` with an entry in its README and in the catalog. Something used once stays in its feature until a second use.
- The main chunk carries the primitives (a few kB). The 150 kB budget is unaffected.
- Sheets (Settings, the Inbox, the resource sheet), menus and toasts still have their own shells. Each is a candidate for a primitive once two of them share one.

## Alternatives considered

- **Keep the CSS classes and only move their rules into one file.** Cheaper, but nothing stops combinations that don't exist (`btn danger` without the colour), and fields can't wire their ids.
- **A workspace package (`packages/ui`).** Only the SPA uses these. A package adds a build boundary with no second consumer. Move it there if one appears.
- **Storybook or Histoire for the catalog.** Both add a large third-party toolchain beside Bun and Vite (ADR-0019). A dev-only page in the app does the job.
