/**
 * Visual regression (E5, docs/plan/15-v0.5.md): one workbench sheet per
 * widget and theme, compared with a reference screenshot in Chromium.
 *
 * A sheet is a grid: a row per level of detail (lib/level.ts), a column per
 * state (loading, empty, healthy, incidents, stale; test/widget-states.ts),
 * each cell the widget at one size, laid out as the board lays out a tile
 * (`.sec` > `.sec-grid` > `.cell`, one grid unit = --unit-max). The page
 * loads the app's global CSS and fonts as src/main.ts does.
 *
 * Nothing may change between two runs: the clock is the fixtures' (Date is
 * faked), times show in UTC, motion is off (`data-motion="none"`, reduced
 * motion, no animations or transitions, no caret), and the shot waits for
 * every font face. Where references live: vitest.visual.config.ts.
 */
/// <reference types="@vitest/browser-playwright" />
import '@chadash/tokens/tokens.css';
import '../../src/styles/fonts.css';
import '../../src/styles/base.css';
import '../../src/ui/ui.css';
import '../../src/styles/board.css';
import '../../src/styles/tiles.css';
import '../../src/styles/chrome.css';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from 'vitest/browser';
import { flushPromises } from '@vue/test-utils';
import { defineComponent, h, nextTick, type PropType } from 'vue';
import { SIZES, type SizeId } from '@chadash/layout';
import { setDisplayTimeZone } from '../../src/lib/format';
import { LEVELS } from '../../src/lib/level';
import { applyTheme, THEMES } from '../../src/lib/theme';
import { itemId, WIDGETS } from '../../src/model/widgets';
import { preloadWidgets } from '../../src/widgets/registry';
import { fakeFetch } from '../fake-fetch';
import { withIncidents } from '../fixtures';
import { harness } from '../harness';
import { InMain, STATES, targetOf, WIDGET_ROUTES } from '../widget-states';

/** The sheet's columns: every state but the hub's graph shape, which looks the same as incidents. */
const COLUMNS = STATES.filter((s) => s.name !== 'hub graph shape');

/** One size per widget: small if it has it, else wide, else its first. */
export function sheetSize(sizes: readonly SizeId[]): SizeId {
  return sizes.includes('small') ? 'small' : sizes.includes('wide') ? 'wide' : sizes[0]!;
}

const STILL = `
*, *::before, *::after { animation: none !important; transition: none !important; caret-color: transparent !important; }
body { margin: 0; background: var(--bg); color: var(--text); }
.vsheet { display: grid; grid-template-columns: auto repeat(${COLUMNS.length}, max-content); gap: var(--gutter); padding: var(--gutter); width: max-content; background: var(--bg); }
.vsheet-h { font: var(--weight-medium) var(--type-micro) / 1 var(--font-num); letter-spacing: var(--tracking-caps); text-transform: uppercase; color: var(--text-3); align-self: end; }
.vsheet-l { writing-mode: vertical-rl; transform: rotate(180deg); text-align: center; }
.vsheet main, .vsheet-col { display: contents; }
/* As StaticBoard.vue styles a board cell: the tile fills its grid area. */
.vsheet .cell { display: grid; min-width: 0; min-height: 0; }
.vsheet .cell > * { min-width: 0; min-height: 0; }
`;

/** One cell: the widget at one level, in a one-tile section the size of its grid units. */
const Cell = defineComponent({
  props: { type: { type: String, required: true }, size: { type: String as PropType<SizeId>, required: true }, level: { type: String, required: true } },
  setup(props) {
    const { w, h: rows } = SIZES[props.size];
    const spec = WIDGETS[props.type]!;
    const item = { id: itemId(props.type, targetOf(props.type, spec.bound)), type: props.type };
    return () =>
      h('section', { class: 'sec', style: { width: `calc(${w} * var(--unit-max) + ${w - 1} * var(--gutter))` }, 'data-level': props.level }, [
        h('div', { class: 'sec-grid', style: { '--scols': String(w), '--u': 'var(--unit-max)', gridTemplateRows: `repeat(${rows}, var(--u))` } }, [
          h('div', { class: 'cell', style: { gridColumn: `1 / span ${w}`, gridRow: `1 / span ${rows}` } }, [h(InMain, { item, size: props.size, level: props.level })]),
        ]),
      ]);
  },
});

/** A state's column: the widget at every level, top to bottom. */
const Column = defineComponent({
  props: { type: { type: String, required: true }, size: { type: String as PropType<SizeId>, required: true }, col: { type: Number, required: true } },
  setup: (props) => () =>
    h(
      'div',
      { class: 'vsheet-col', 'data-col': props.col },
      LEVELS.map((level, row) => h(Cell, { type: props.type, size: props.size, level, style: { gridRow: String(row + 2), gridColumn: String(props.col + 2) } })),
    ),
});

/** Builds the sheet for `type` in document.body; returns a teardown. */
async function mountSheet(type: string): Promise<() => void> {
  const size = sheetSize(WIDGETS[type]!.sizes);
  const sheet = document.createElement('div');
  sheet.className = 'vsheet';
  sheet.dataset['testid'] = 'sheet';
  sheet.append(el('span', 'vsheet-h', ''));
  for (const state of COLUMNS) sheet.append(el('span', 'vsheet-h', state.name));
  LEVELS.forEach((level, i) => {
    const label = el('span', 'vsheet-h vsheet-l', level);
    label.style.gridRow = String(i + 2);
    label.style.gridColumn = '1';
    sheet.append(label);
  });
  document.body.append(sheet);

  const stops: (() => void)[] = [];
  COLUMNS.forEach((state, col) => {
    const hub = harness(state.data);
    if (state.stale) hub.ws.drop();
    const w = hub.mountIn(Column, { type, size, col });
    // Test Utils mounts into a div of its own in <body>: move it into the sheet, where Column's cells place themselves.
    const wrapper = document.querySelector(`[data-col="${col}"]`)!.parentElement!;
    wrapper.style.display = 'contents';
    sheet.append(wrapper);
    stops.push(() => {
      w.unmount();
      hub.live.stop();
    });
  });
  await flushPromises();
  await nextTick();
  await settled();
  return () => {
    for (const stop of stops) stop();
    document.body.innerHTML = '';
  };
}

function el(tag: string, cls: string, text: string): HTMLElement {
  const e = document.createElement(tag);
  e.className = cls;
  e.textContent = text;
  return e;
}

/** Every font face the themes name, loaded, then two frames for layout to settle. */
async function settled() {
  const faces = ['Instrument Sans', 'Geist Mono', 'Doto', 'Schibsted Grotesk', 'Newsreader'];
  await Promise.all(faces.flatMap((f) => [document.fonts.load(`400 16px "${f}"`), document.fonts.load(`700 16px "${f}"`)]));
  await document.fonts.ready;
  for (let i = 0; i < 2; i++) await new Promise((r) => requestAnimationFrame(() => r(undefined)));
}

/** Registers the hooks and tests for shard `n` of `of`: every widget whose catalog index is n - 1 modulo `of`. */
export function visualSheets(n: number, of: number) {
  beforeAll(async () => {
    await preloadWidgets();
    const style = document.createElement('style');
    style.textContent = STILL;
    document.head.append(style);
    document.documentElement.dataset['motion'] = 'none';
  });
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['Date'], now: new Date(withIncidents.now) });
    setDisplayTimeZone('UTC');
    fakeFetch(WIDGET_ROUTES);
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  for (const type of Object.keys(WIDGETS).filter((_, i) => i % of === n - 1)) {
    describe(type, () => {
      for (const theme of THEMES) {
        it(theme.id, async () => {
          applyTheme(theme.id);
          const teardown = await mountSheet(type);
          try {
            await expect.element(page.getByTestId('sheet')).toMatchScreenshot(`${type}/${theme.id}`, {
              screenshotOptions: { animations: 'disabled', caret: 'hide', scale: 'css' },
              // Font hinting and anti-aliasing move a few pixels between runs of the same image.
              comparatorOptions: { threshold: 0.1, allowedMismatchedPixelRatio: 0.0005 },
            });
          } finally {
            teardown();
          }
        });
      }
    });
  }
}
