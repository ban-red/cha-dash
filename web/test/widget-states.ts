/**
 * The per-widget state matrix the widget sweeps share: the axe sweep
 * (a11y-widgets.ts, in happy-dom) and the visual sheets (visual/sheet.ts, in
 * Chromium). A state is the data the fake hub socket hands the page.
 */
import { defineComponent, h } from 'vue';
import type { SizeId } from '@chadash/layout';
import WidgetHost from '../src/widgets/WidgetHost.vue';
import { provideLevel, type Level } from '../src/lib/level';
import { docs, healthy, withIncidents } from './fixtures';

/** A Demo Home target for each widget that shows one (`type@target`), as in widgets.test.ts. */
export const TARGETS: Record<string, string> = {
  host: 'unraid:magpie',
  unraidhost: 'unraid:magpie',
  arr: 'app:sonarr',
  service: 'homepage:servers/proxmox',
  metric: 'pve:stork#mem.pct',
  storage: 'pve:owl/storage/local-lvm',
};

/** The target a bound widget shows: its entry in TARGETS, else the first resource. */
export function targetOf(type: string, bound: boolean | undefined): string | undefined {
  return bound ? (TARGETS[type] ?? Object.keys(docs(withIncidents).graph.resources)[0]) : undefined;
}

/** A tile in its page: always inside the front page's <main>, at the level its surface gives (lib/level.ts; full by default). */
export const InMain = defineComponent({
  props: { item: { type: Object, required: true }, size: { type: String, required: true }, level: { type: String, default: 'full' } },
  setup: (props) => {
    provideLevel(() => props.level as Level);
    return () => h('main', [h(WidgetHost, { item: props.item as { id: string; type: string }, size: props.size as SizeId })]);
  },
});

export const EMPTY = { graph: { resources: {}, relations: {} }, metrics: { series: {} }, inbox: { now: withIncidents.now, incidents: {} } };

export type State = { name: string; data?: Record<string, unknown>; stale?: boolean };
export const STATES: State[] = [
  { name: 'loading' },
  { name: 'empty', data: EMPTY },
  { name: 'healthy', data: docs(healthy) },
  { name: 'incidents', data: docs(withIncidents) },
  { name: 'stale', data: docs(withIncidents), stale: true },
  { name: 'hub graph shape', data: docs(withIncidents, 'hub') },
];

/** The fetch routes a widget calls on mount (the Homepage import's tile, for `service`). */
export const WIDGET_ROUTES = {
  'GET /api/import/homepage': {
    status: 200,
    body: {
      record: {
        board_id: 'b1',
        tiles: [{ item_id: 'service@homepage:servers/proxmox', name: 'Proxmox', href: 'https://pve.example.com:8006', description: 'Hypervisor', origin: 'Servers → Proxmox' }],
      },
    },
  },
};
