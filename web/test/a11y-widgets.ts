/**
 * WCAG 2.2 A/AA (axe-core) for every widget in the catalog, at every size it
 * supports, in each state the page can show it in: loading, empty, healthy,
 * with today's incidents (attention, down, repair), stale, and with the
 * hub's graph shape. Contrast is checked on the tokens (see test/axe.ts).
 *
 * The sweep is split across a11y-widgets-<n>.test.ts so Vitest runs the
 * shards in parallel workers: one file took 18 s, a quarter of the suite's wall time.
 */
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { nextTick } from 'vue';
import type { SizeId } from '@chadash/layout';
import { preloadWidgets } from '../src/widgets/registry';
import { setDisplayTimeZone } from '../src/lib/format';
import { LEVELS } from '../src/lib/level';
import { itemId, WIDGETS } from '../src/model/widgets';
import { axeViolations } from './axe';
import { fakeFetch } from './fake-fetch';
import { harness } from './harness';
import { InMain, STATES, targetOf, WIDGET_ROUTES } from './widget-states';

export { InMain };

/** Registers the hooks every shard needs: call it at the top of the test file. */
export function axeHooks() {
  // The on-demand widgets, fetched once, so each mounts with its content rather than a skeleton.
  beforeAll(() => preloadWidgets());

  beforeEach(() => {
    setDisplayTimeZone('UTC');
    fakeFetch(WIDGET_ROUTES);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    document.body.innerHTML = '';
  });
}

/** Shard `n` of `of`: every widget whose index in the catalog is n - 1 modulo `of`. */
export function axeWidgets(n: number, of: number) {
  describe('every widget, size and state passes axe', () => {
    for (const [type, spec] of Object.entries(WIDGETS).filter((_, i) => i % of === n - 1)) {
      it(type, async () => {
        const problems: string[] = [];
        for (const state of STATES) {
          for (const size of spec.sizes as SizeId[]) {
            const h = harness(state.data);
            if (state.stale) h.ws.drop();
            const target = targetOf(type, spec.bound);
            const w = h.mountIn(InMain, { item: { id: itemId(type, target), type }, size });
            await flushPromises();
            await nextTick();
            for (const v of await axeViolations(w.element.parentElement ?? document.body)) problems.push(`${state.name}, ${size}: ${v}`);
            w.unmount();
            h.live.stop();
            document.body.innerHTML = '';
          }
        }
        expect(problems).toEqual([]);
      }, 30_000); // every size, state and widget runs axe: Panel and Column made that longer
    }
  });
}

/**
 * Shard `n` of `of` of the levels sweep: every widget at the compact,
 * simplified and ink levels, at its smallest and largest sizes, in each
 * state. Full is the sweep above.
 */
export function axeLevels(n: number, of: number) {
  describe('every widget passes axe at every level of detail', () => {
    for (const [type, spec] of Object.entries(WIDGETS).filter((_, i) => i % of === n - 1)) {
      it(type, async () => {
        const problems: string[] = [];
        const sizes = [...new Set([spec.sizes[0], spec.sizes[spec.sizes.length - 1]])] as SizeId[];
        for (const level of LEVELS.filter((l) => l !== 'full')) {
          for (const state of STATES) {
            for (const size of sizes) {
              const h = harness(state.data);
              if (state.stale) h.ws.drop();
              const target = targetOf(type, spec.bound);
              const w = h.mountIn(InMain, { item: { id: itemId(type, target), type }, size, level });
              await flushPromises();
              await nextTick();
              const tile = w.element.querySelector('.tile');
              if (tile && !tile.classList.contains(`lvl-${level}`)) problems.push(`${level}, ${state.name}, ${size}: the tile isn't marked lvl-${level}`);
              for (const v of await axeViolations(w.element.parentElement ?? document.body)) problems.push(`${level}, ${state.name}, ${size}: ${v}`);
              w.unmount();
              h.live.stop();
              document.body.innerHTML = '';
            }
          }
        }
        expect(problems).toEqual([]);
      }, 60_000);
    }
  });
}
