/**
 * The seven scenarios, as data. Each starts from the dogfood front page at
 * 12 columns. Adapters replay the steps; the harness checks every state.
 */
import { PIHOLE } from './dogfood.ts';
import type { Scenario, Step } from './types.ts';

export * from './types.ts';
export { DOGFOOD, PIHOLE, sixtyWidgetBoard } from './dogfood.ts';

/** Ten mixed operations for the undo scenario: moves, a cross-section move, resizes and an insert. */
export const TEN_OPS: readonly Step[] = [
  { op: 'move', item: 'fleet', to: { section: 'infra', x: 4, y: 0 } },
  { op: 'resize', item: 'plex', size: 'large' },
  { op: 'move', item: 'backups', to: { section: 'infra', x: 0, y: 0 } },
  { op: 'resize', item: 'containers', size: 'wide' },
  { op: 'move', item: 'traefik', to: { section: 'media', x: 0, y: 0 } },
  { op: 'resize', item: 'calendar', size: 'tall' },
  { op: 'move', item: 'homeassistant', to: { section: 'home', x: 0, y: 0 } },
  { op: 'insert', item: PIHOLE, near: { section: 'infra', x: 4, y: 2 } },
  { op: 'resize', item: 'fleet', size: 'wide' },
  { op: 'move', item: 'downloads', to: { section: 'media', x: 0, y: 0 } },
];

export const SCENARIOS: readonly Scenario[] = [
  {
    id: 'front-page',
    title: 'The dogfood front page',
    expect: '15 widgets in 4 sections, packed in reading order, every size a supported one, no overlap, no hole.',
    steps: [],
  },
  {
    id: 'drag-large',
    title: 'Drag a Large tile across a section',
    expect: 'Fleet (Large) lands at the right half of Infrastructure; neighbours reflow; no overlap, no hole.',
    steps: [{ op: 'move', item: 'fleet', to: { section: 'infra', x: 4, y: 0 } }],
  },
  {
    id: 'resize-next-to-full-row',
    title: 'Resize Wide → Large next to a full row',
    expect: 'Now playing grows to 4×4 beside the full Sonarr/Radarr row; the rest of Media reflows below or beside it.',
    steps: [{ op: 'resize', item: 'plex', size: 'large' }],
  },
  {
    id: 'insert-full',
    title: 'Insert into a full board',
    expect: 'With no hint the widget lands without moving anything; with a hint (Infrastructure, row 3) it lands there and the section reflows.',
    steps: [
      { op: 'insert', item: PIHOLE },
      { op: 'insert', item: { ...PIHOLE, id: 'pihole-2' }, near: { section: 'infra', x: 4, y: 2 } },
    ],
  },
  {
    id: 'derive',
    title: 'Derive the 8- and 4-column layouts',
    expect: 'Supported sizes only (a hero becomes a large), sections keep their structure where they fit, the same answer whichever way you get there.',
    steps: [
      { op: 'columns', columns: 8 },
      { op: 'columns', columns: 4 },
    ],
  },
  {
    id: 'undo-10',
    title: 'Undo after 10 operations',
    expect: 'Ten undos bring back the exact starting layout; each undo the exact state before that operation.',
    steps: [...TEN_OPS, { op: 'undo', times: 10 }],
  },
  {
    id: 'round-trip',
    title: 'Serialize and restore',
    expect: 'A phone-only tweak and an edited desktop layout survive serialize → restore, and serializing again gives the identical string.',
    steps: [
      { op: 'move', item: 'fleet', to: { section: 'infra', x: 4, y: 0 } },
      { op: 'columns', columns: 4 },
      { op: 'move', item: 'uptimekuma', to: { section: 'infra', x: 0, y: 0 } },
      { op: 'roundTrip' },
    ],
  },
];
