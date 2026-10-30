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
import { defineComponent, h, nextTick } from 'vue';
import type { SizeId } from '@chadash/layout';
import WidgetHost from '../src/widgets/WidgetHost.vue';
import { preloadWidgets } from '../src/widgets/registry';
import { setDisplayTimeZone } from '../src/lib/format';
import { itemId, WIDGETS } from '../src/model/widgets';
import { axeViolations } from './axe';
import { fakeFetch } from './fake-fetch';
import { docs, healthy, withIncidents } from './fixtures';
import { harness } from './harness';

/** A Demo Home target for each widget that shows one (`type@target`), as in widgets.test.ts. */
const TARGETS: Record<string, string> = {
  host: 'unraid:magpie',
  unraidhost: 'unraid:magpie',
  arr: 'app:sonarr',
  service: 'homepage:servers/proxmox',
  metric: 'pve:stork#mem.pct',
  storage: 'pve:owl/storage/local-lvm',
};

/** A tile in its page: always inside the front page's <main>. */
export const InMain = defineComponent({
  props: { item: { type: Object, required: true }, size: { type: String, required: true } },
  setup: (props) => () => h('main', [h(WidgetHost, { item: props.item as { id: string; type: string }, size: props.size as SizeId })]),
});

const EMPTY = { graph: { resources: {}, relations: {} }, metrics: { series: {} }, inbox: { now: withIncidents.now, incidents: {} } };

type State = { name: string; data?: Record<string, unknown>; stale?: boolean };
const STATES: State[] = [
  { name: 'loading' },
  { name: 'empty', data: EMPTY },
  { name: 'healthy', data: docs(healthy) },
  { name: 'incidents', data: docs(withIncidents) },
  { name: 'stale', data: docs(withIncidents), stale: true },
  { name: 'hub graph shape', data: docs(withIncidents, 'hub') },
];

/** Registers the hooks every shard needs: call it at the top of the test file. */
export function axeHooks() {
  // The on-demand widgets, fetched once, so each mounts with its content rather than a skeleton.
  beforeAll(() => preloadWidgets());

  beforeEach(() => {
    setDisplayTimeZone('UTC');
    fakeFetch({
      'GET /api/import/homepage': {
        status: 200,
        body: {
          record: {
            board_id: 'b1',
            tiles: [{ item_id: 'service@homepage:servers/proxmox', name: 'Proxmox', href: 'https://pve.example.com:8006', description: 'Hypervisor', origin: 'Servers → Proxmox' }],
          },
        },
      },
    });
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
            const target = spec.bound ? (TARGETS[type] ?? Object.keys(docs(withIncidents).graph.resources)[0]) : undefined;
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
