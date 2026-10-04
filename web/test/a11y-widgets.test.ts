/**
 * WCAG 2.2 A/AA (axe-core) for every widget in the catalog, at every size it
 * supports, in each state the page can show it in: loading, empty, healthy,
 * with today's incidents (attention, down, repair), stale, and with the
 * hub's graph shape. Contrast is checked on the tokens (see test/axe.ts).
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { enableAutoUnmount, flushPromises } from '@vue/test-utils';
import { defineComponent, h, nextTick } from 'vue';
import type { SizeId } from '@chadash/layout';
import WidgetHost from '../src/widgets/WidgetHost.vue';
import { setDisplayTimeZone } from '../src/lib/format';
import { itemId, WIDGETS } from '../src/model/widgets';
import { axeViolations } from './axe';
import { fakeFetch } from './fake-fetch';
import { docs, healthy, withIncidents } from './fixtures';
import { harness } from './harness';

/** A resource for each widget that shows one (`type@resource`). */
const TARGETS: Record<string, string> = {
  host: 'unraid:jupiter',
  arr: 'app:sonarr',
  service: 'homepage:servers/proxmox',
};

/** A tile in its page: always inside the front page's <main>. */
const InMain = defineComponent({
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

enableAutoUnmount(afterEach);

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

it('sees what it checks: a planted violation in a mounted tile is caught', async () => {
  const h = harness(docs(withIncidents));
  const w = h.mountIn(InMain, { item: { id: 'fleet', type: 'fleet' }, size: 'large' });
  await flushPromises();
  const root = w.element.parentElement!;
  expect(root.querySelector('.tile')).not.toBeNull();
  expect(await axeViolations(root)).toEqual([]);
  root.querySelector('.tile')!.insertAdjacentHTML('beforeend', '<button type="button"></button>');
  expect((await axeViolations(root)).join('\n')).toContain('button-name');
  h.live.stop();
});

describe('every widget, size and state passes axe', () => {
  for (const [type, spec] of Object.entries(WIDGETS)) {
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
    });
  }
});
