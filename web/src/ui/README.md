# ui/: the primitives

Cha Dash's reusable visual building blocks ([ADR-0032](../../../docs/adr/0032-ui-primitives.md)). Three layers, each using only the one below:

1. **Tokens:** [`@chadash/tokens`](../../../design/tokens/README.md), the colours, sizes and motion every theme defines.
2. **Primitives:** this folder. Semantic tokens only, no feature logic.
3. **Features:** `components/`, `settings/`, `board/`, `widgets/` and the rest. They place primitives and lay them out. They don't restyle them.

See them all, in every variant and theme, at **`/dev/ui`** on the Vite dev server (`src/dev/UiCatalog.vue`). Production builds drop it.

## What's here

| File | Use it for | Notes |
|---|---|---|
| `Button.vue` (`UiButton`) | Every button, and links that look like one | `variant`: `default`, `primary`, `ghost`, `danger`, `danger-solid`. `size`: `md`, `sm`. `href` renders an `<a>`. `type` defaults to `button`. Exposes `focus()`. A tier tag goes inside as `<span class="tier">T3</span>`. |
| `CloseButton.vue` (`UiCloseButton`) | The round × | `label` names what it does: "Close settings", "Cancel", "Dismiss". |
| `Field.vue` (`UiField`) | A label, a control, help or an error | The default slot gets `control` (`id`, `aria-describedby`, `aria-invalid`): bind it with `v-bind="control"`. `error` replaces the help; `invalid` keeps the help and turns it red; `alert` announces the error; `optional`, `#aside`, `check`, `hideLabel`, `id`. The help's id is the control's id plus `-help`. |
| `Dialog.vue` (`UiDialog`) | A native modal dialog | `title` or `#title`, `#lead`, `#head`, the body, `#foot`. `size`: `sm` 460, `md` 520, `lg` 720. `as="form"` emits `submit`, and `requestSubmit()` submits it. Emits `close` on Esc and on the ×; the parent unmounts it. |
| `Menu.vue` (`UiMenu`) | A dropdown from a button: the tile, section and settings menus | `label` names the trigger and the panel; `#trigger` is the icon (three dots by default). The default slot gets `{ view, goto, close }` and renders the current view: a `role="menu"` of `.menu-item`s (`.menu-val`, `.menu-sep`, `.danger`), a `.menu-confirm`, or a small form. It opens on `menu`; Escape steps back to it, then closes. `v-model:view`, `popClass`; exposes `close(refocus)` and `goto(view)`. Class falls through to the trigger. |
| `Sheet.vue` (`UiSheet`) | A modal sheet: the Inbox, a resource's sheet | `open` (mount it once and toggle, so the exit plays), `from` `top` or `right`, `size` `md` 460 or `lg` 520. A top-right panel on desktop, a bottom card on a phone that a drag dismisses. Emits `close` on Esc, the scrim and the drag; `open` with the panel (focus goes there unless the handler moves it in). Keeps Tab inside, returns focus on close. Class and `aria-labelledby` fall through to the panel; set its padding with `--sheet-pad`. |
| `marks/Glyph.vue` | State as shape + word + colour | `kind`, and `label` when it stands alone. |
| `marks/Meter.vue` | A bar for a value in a range | `warn`, `crit`, `tick`, `thin` or `thick`. |
| `marks/MiniGraph.vue` | A tile's trend line | See [web/README.md](../../README.md#widgets). |
| `useModal.ts` | `UiDialog`'s open-on-mount | Use `UiDialog` instead. |
| `ui.css` | The styles for buttons, ×, fields, menus and marks | Loaded by `main.ts` straight after `base.css`, so every feature stylesheet comes later. |
| `dialog.css` | `UiDialog`'s shell | Loads with the first dialog chunk. |
| `sheet.css` | `UiSheet`'s panel | Loads with the first sheet's chunk. The layer and scrim are chrome.css's. |

## Rules

- **Import by path**, as in `import UiButton from '../ui/Button.vue'`. There's no barrel file: one would pull `UiDialog`'s CSS into the first-paint chunk.
- **The DOM keeps its class names** (`.btn.primary`, `.x`, `.field`, `.dlg`). Tests and layout CSS may select them. Don't add rules for them outside this folder, except to lay one out (a margin, a width).
- **`test/ui-boundary.test.ts`** fails when a feature template writes `class="btn"`, `class="x"`, `class="field"`, a `<dialog>` or a menu panel (`class="menu-pop"`) by hand. Its allow-list names each exception and why.
- **Adding a primitive:** it should have a second user first. Put it here, add a row above, show every state in the catalog, and add tests to `test/ui.test.ts`, including axe.
- **Fallthrough attributes are typed.** `vueCompilerOptions.fallthroughAttributes` in `web/tsconfig.json` lets vue-tsc check `disabled`, `aria-*` and listeners against each primitive's root element.
- **Not here yet:** the Settings sheet, the first-run wizard, the T2 confirm and toasts still have their own shells. `SchemaField.vue` builds on the `.field` CSS directly, because it renders fields from a JSON Schema.
