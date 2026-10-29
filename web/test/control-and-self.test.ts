/**
 * Track 10C: control credentials in the integration form, where Cha Dash
 * runs (and "Cha Dash itself" in a blast radius), and the hub's cached icons
 * on tiles.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { enableAutoUnmount, flushPromises } from '@vue/test-utils';
import { defineComponent, h, nextTick, type Component } from 'vue';
import IntegrationEditor from '../src/settings/IntegrationEditor.vue';
import HubPlace from '../src/settings/HubPlace.vue';
import ConfirmDialog from '../src/components/ConfirmDialog.vue';
import TypeConfirmDialog from '../src/components/TypeConfirmDialog.vue';
import Tile, { type TileStatus } from '../src/components/Tile.vue';
import { route, settingsPath, syncRoute } from '../src/lib/router';
import type { JsonSchema } from '../src/lib/schema';
import { actStatus, controlKeys, modeConsequence, type ControlSpec } from '../src/model/control';
import { hubPlace, isHub, placeWords } from '../src/model/hub';
import { hitsHub, hubFirst, impactName } from '../src/model/words';
import type { GraphDoc } from '../src/model/types';
import type { Recipe } from '../src/settings/recipe';
import { backupPreviewOf } from '../src/model/backupPlan';
import { resetIntegrations } from '../src/stores/integrations';
import { iconFor, iconMap, loadIcons, resetIcons } from '../src/stores/icons';
import { provideTile } from '../src/stores/tiles';
import { axeViolations } from './axe';
import { fakeFetch } from './fake-fetch';
import { docs, graphDoc, healthy } from './fixtures';
import { harness } from './harness';

enableAutoUnmount(afterEach);

const recipe = (title: string, fills: Record<string, string> = {}): Recipe => ({
  title,
  summary: `${title}, in short.`,
  access: 'scoped',
  params: [],
  steps: [{ title: 'Make it', blocks: [{ kind: 'text', text: 'Do the thing.' }] }],
  notes: [],
  fills,
  grants: [],
});

const PVE_SCHEMA: JsonSchema = {
  type: 'object',
  required: ['url', 'token_id', 'token_secret'],
  properties: {
    url: { type: 'string', format: 'uri', title: 'URL' },
    token_id: { type: 'string', title: 'Token ID' },
    token_secret: { type: 'string', writeOnly: true, title: 'Token secret' },
    control_token_id: { type: 'string', title: 'Control token ID', 'x-chadash-group': 'control' },
    control_token_secret: { type: 'string', writeOnly: true, title: 'Control token secret', 'x-chadash-group': 'control' },
  },
};
const PVE_CONTROL: ControlSpec = {
  fields: [
    { field: 'control_token_id', replaces: 'token_id' },
    { field: 'control_token_secret', replaces: 'token_secret' },
  ],
  missing: 'Add a control token to let Cha Dash act on {instance}.',
};

const UNRAID_SCHEMA: JsonSchema = {
  type: 'object',
  required: ['url', 'api_key'],
  properties: {
    url: { type: 'string', format: 'uri', title: 'URL' },
    api_key: { type: 'string', writeOnly: true, title: 'API key' },
    mode: {
      type: 'string',
      title: 'Read-only, or let Cha Dash control Unraid?',
      enum: ['read_only', 'control'],
      oneOf: [
        { const: 'read_only', title: 'Read-only' },
        { const: 'control', title: 'Control: start, stop and update containers and VMs' },
      ],
      default: 'read_only',
    },
  },
};
const UNRAID_CONTROL: ControlSpec = { mode: 'mode', mode_on: 'control', grants: ['docker', 'vms'] };

function hub() {
  return fakeFetch({
    'GET /api/integrations/catalog': {
      status: 200,
      body: [
        { id: 'proxmox', name: 'Proxmox VE', version: '0.2.0', tier: 'gold', config_schema: PVE_SCHEMA, control: PVE_CONTROL, recipe: { ...recipe('A read-only token'), control: recipe('A control token', { control_token_id: '{{user}}!{{token}}' }) } },
        { id: 'unraid', name: 'Unraid', version: '0.2.0', tier: 'silver', config_schema: UNRAID_SCHEMA, control: UNRAID_CONTROL, recipe: recipe('An API key') },
      ],
    },
    'GET /api/integrations/instances': {
      status: 200,
      body: [
        { id: 'i_pve', integration: 'proxmox', name: 'io', status: 'ok', warnings: [] },
        { id: 'i_un', integration: 'unraid', name: 'jupiter', status: 'ok', warnings: [] },
      ],
    },
    'GET /api/integrations/instances/i_pve': {
      status: 200,
      body: {
        id: 'i_pve',
        integration: 'proxmox',
        name: 'io',
        status: 'ok',
        config: { url: 'https://io.lab.example:8006', token_id: 'chadash@pve!read' },
        secrets: { token_secret: { set: true }, control_token_secret: { set: false } },
        warnings: [{ code: 'control_available', severity: 'info', message: 'This connection only reads. Add a control credential to let Cha Dash act.' }],
      },
    },
    'GET /api/integrations/instances/i_un': {
      status: 200,
      body: { id: 'i_un', integration: 'unraid', name: 'jupiter', status: 'ok', config: { url: 'https://jupiter.lab.example', mode: 'read_only' }, secrets: { api_key: { set: true } }, warnings: [] },
    },
  });
}

beforeEach(() => {
  history.replaceState(null, '', '/settings/integrations');
  syncRoute();
  resetIntegrations();
  resetIcons();
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

/** Inside a main landmark, as the app renders every view. */
const inMain = (c: Component, props: Record<string, unknown> = {}) => defineComponent({ setup: () => () => h('main', { 'aria-label': 'Settings' }, [h(c, props)]) });

async function editor(props: Record<string, unknown>) {
  const hs = harness();
  const w = hs.mountIn(inMain(IntegrationEditor, props));
  await flushPromises();
  await nextTick();
  return { ...hs, w };
}

describe('control credentials, the model', () => {
  it('groups the control fields and the mode, and says whether it can act', () => {
    expect(controlKeys(PVE_CONTROL, PVE_SCHEMA)).toEqual(['control_token_id', 'control_token_secret']);
    expect(controlKeys(UNRAID_CONTROL, UNRAID_SCHEMA)).toEqual(['mode']);
    expect(controlKeys(undefined, PVE_SCHEMA)).toEqual([]);
    const none = new Set<string>();
    expect(actStatus(PVE_CONTROL, PVE_SCHEMA, {}, none).state).toBe('reads');
    expect(actStatus(PVE_CONTROL, PVE_SCHEMA, { control_token_id: 'a!b' }, none)).toEqual({ state: 'partial', missing: ['control_token_secret'] });
    expect(actStatus(PVE_CONTROL, PVE_SCHEMA, { control_token_id: 'a!b' }, new Set(['control_token_secret'])).state).toBe('acts');
    expect(actStatus(PVE_CONTROL, PVE_SCHEMA, { control_token_id: 'a!b' }, none, { control_token_secret: 'typed' }).state).toBe('acts');
    expect(actStatus(UNRAID_CONTROL, UNRAID_SCHEMA, { mode: 'control' }, none).state).toBe('acts');
    expect(actStatus(UNRAID_CONTROL, UNRAID_SCHEMA, { mode: 'read_only' }, none).state).toBe('reads');
    expect(modeConsequence(true, 'jupiter')).toContain('audit log');
    expect(modeConsequence(false, 'jupiter')).toContain('only reads');
  });
});

describe('Let Cha Dash act', () => {
  it('folds the control token into its own section, says it only reads, and shows the control recipe beside it', async () => {
    hub();
    await editor({ instanceRef: 'i_pve' });
    const section = document.getElementById('control')!;
    expect(section).not.toBeNull();
    // The control fields live in the section, not with the read credential.
    expect(section.querySelector('#f-control_token_id')).not.toBeNull();
    expect(section.querySelector('#f-control_token_secret')).not.toBeNull();
    expect(section.querySelector('#f-token_id')).toBeNull();
    expect(section.querySelector('details')!.open).toBe(false);
    expect(section.querySelector('.ctl-state')!.textContent).toContain('Reads only');
    expect(section.textContent).toContain('Cha Dash only reads io.');
    expect(section.textContent).toContain('Add a control credential');
    const aside = document.querySelector('.ed-control-recipe')!;
    expect(aside.textContent).toContain('To let Cha Dash act');
    expect(aside.textContent).toContain('A control token');
    expect(await axeViolations()).toEqual([]);
  });

  it('opens at ?section=control (an unavailable action’s recipe link) and says what saving would change', async () => {
    hub();
    await editor({ instanceRef: 'i_pve', section: 'control' });
    const section = document.getElementById('control')!;
    expect(section.querySelector('details')!.open).toBe(true);
    expect(document.activeElement).toBe(section.querySelector('summary'));
    expect((document.querySelector('.ed-control-recipe details') as HTMLDetailsElement).open).toBe(true);
    const id = document.getElementById('f-control_token_id') as HTMLInputElement;
    id.value = 'chadash@pve!control';
    id.dispatchEvent(new Event('input'));
    await nextTick();
    expect(section.textContent).toContain('Still empty: control_token_secret');
    const secret = document.getElementById('f-control_token_secret') as HTMLInputElement;
    secret.value = 'not-a-real-secret-value';
    secret.dispatchEvent(new Event('input'));
    await nextTick();
    expect(section.querySelector('.ctl-now')!.textContent).toContain('After you save: cha dash can act on io.');
    expect(await axeViolations()).toEqual([]);
  });

  it('presents Unraid’s mode with what each choice means', async () => {
    hub();
    await editor({ instanceRef: 'i_un', section: 'control' });
    const section = document.getElementById('control')!;
    const radios = [...section.querySelectorAll<HTMLInputElement>('input[type="radio"]')];
    expect(radios.length).toBe(2);
    expect(section.querySelector('.ctl-means')!.textContent).toContain('only reads jupiter');
    radios[1]!.checked = true;
    radios[1]!.dispatchEvent(new Event('change'));
    await nextTick();
    expect(section.querySelector('.ctl-means')!.textContent).toContain('may act on jupiter');
    expect(section.querySelector('.ctl-now')!.textContent).toContain('After you save');
    expect(await axeViolations()).toEqual([]);
  });

  it('an unavailable refusal’s link opens the instance’s control section', () => {
    hub();
    const hs = harness();
    hs.ui.openSettings('integrations', 'i_pve', { section: 'control' });
    expect(route.value.path).toBe('/settings/integrations/i_pve');
    expect(route.value.query['section']).toBe('control');
    expect(settingsPath('integrations', 'i_pve', { section: 'control' })).toBe('/settings/integrations/i_pve?section=control');
  });
});

// A real hub's graph: Cha Dash in a container on pvedocker, a VM on io, and a router.
const REAL: GraphDoc = {
  resources: {
    'chadash:hub': { id: 'chadash:hub', kind: 'app', name: 'Cha Dash', attrs: { self: true, version: '0.2.0' } },
    'docker:i_d/container/chadash': { id: 'docker:i_d/container/chadash', kind: 'container', name: 'chadash' },
    'docker:i_d/engine': { id: 'docker:i_d/engine', kind: 'engine', name: 'pvedocker' },
    'unraid:i_u/container/chadash': { id: 'unraid:i_u/container/chadash', kind: 'container', name: 'chadash' },
    'proxmox:i_p/qemu/105': { id: 'proxmox:i_p/qemu/105', kind: 'vm', name: 'pvedocker' },
    'proxmox:i_p/node/io': { id: 'proxmox:i_p/node/io', kind: 'host', name: 'io' },
    'opnsense:i_o/router': { id: 'opnsense:i_o/router', kind: 'router', name: 'pluto' },
  },
  relations: {
    a: { from: 'chadash:hub', type: 'runs_on', to: 'docker:i_d/container/chadash' },
    b: { from: 'docker:i_d/container/chadash', type: 'runs_on', to: 'docker:i_d/engine' },
    c: { from: 'docker:i_d/engine', type: 'runs_on', to: 'proxmox:i_p/qemu/105' },
    d: { from: 'proxmox:i_p/qemu/105', type: 'runs_on', to: 'proxmox:i_p/node/io' },
  },
};

describe('where Cha Dash runs', () => {
  it('walks the hub’s chain, across same_as, and names the router', () => {
    const p = hubPlace(REAL);
    expect(p.chain.map(placeWords)).toEqual(['container chadash', 'Docker on pvedocker', 'VM pvedocker', 'host io']);
    expect(p.router?.name).toBe('pluto');
    // Only Unraid sees the container, and the correlator joined it to Docker's.
    const viaSameAs: GraphDoc = {
      resources: REAL.resources,
      relations: {
        a: { from: 'chadash:hub', type: 'runs_on', to: 'unraid:i_u/container/chadash' },
        s: { from: 'docker:i_d/container/chadash', type: 'same_as', to: 'unraid:i_u/container/chadash' },
        b: REAL.relations['b']!,
      },
    };
    expect(hubPlace(viaSameAs).chain.map((r) => r.id)).toEqual(['unraid:i_u/container/chadash', 'docker:i_d/engine']);
    expect(hubPlace({ resources: {}, relations: {} }).chain).toEqual([]);
    // The Demo Home's stand-in.
    const demo = graphDoc(healthy);
    expect(isHub(demo, 'app:chadash')).toBe(true);
    expect(hubPlace(demo).chain.map((r) => r.name)).toEqual(['chadash', 'pvedocker', 'pvedocker', 'mars-1', 'mars']);
  });

  it('shows the chain in Settings, and says how to find it when it can’t', async () => {
    const hs = harness(docs(healthy));
    hs.mountIn(inMain(HubPlace));
    await flushPromises();
    const items = [...document.querySelectorAll('.hp-chain li')].map((li) => li.textContent?.replace(/\s+/g, ' ').trim());
    expect(items[0]).toBe('Cha Dash');
    expect(items[1]).toBe('→container chadash');
    expect(items.at(-1)).toContain('router OPNsense');
    expect(await axeViolations()).toEqual([]);
    document.body.innerHTML = '';
    const empty = harness({ graph: { resources: {}, relations: {} } });
    empty.mountIn(inMain(HubPlace));
    await flushPromises();
    expect(document.querySelector('.hub-place .st-note')!.textContent).toContain('can’t tell where it runs yet');
    expect(await axeViolations()).toEqual([]);
  });

  it('labels the hub “Cha Dash itself” and puts it first', () => {
    expect(impactName(REAL, 'chadash:hub')).toBe('Cha Dash itself');
    expect(impactName(graphDoc(healthy), 'app:chadash')).toBe('Cha Dash itself');
    const xs = [
      { resource: 'docker:i_d/container/chadash', via: 'runs_on', depth: 1 },
      { resource: 'chadash:hub', via: 'runs_on', depth: 2 },
    ];
    expect(hubFirst(REAL, xs).map((x) => x.resource)).toEqual(['chadash:hub', 'docker:i_d/container/chadash']);
    expect(hitsHub(REAL, ['proxmox:i_p/node/io'])).toBe(false);
  });

  it('warns in the hold ring and the typed confirmation when the hub goes down too', async () => {
    const hs = harness({ graph: REAL });
    const impact = [
      { resource: 'docker:i_d/container/chadash', via: 'runs_on', depth: 1 },
      { resource: 'chadash:hub', via: 'runs_on', depth: 2 },
    ];
    const Page = defineComponent({ setup: () => () => [h('main', { 'aria-label': 'Front page' }), h(ConfirmDialog), h(TypeConfirmDialog)] });
    hs.mountIn(Page);
    hs.ui.state.confirm = {
      kind: 'hold',
      req: { action: 'docker.container.stop', target: 'docker:i_d/engine', label: 'Stop chadash' },
      action: { id: 'docker.container.stop', label: 'Stop chadash', tier: 'T2', target: 'docker:i_d/engine', impact },
      plan: { id: 'p_1', hash: 'sha256:x', action: 'docker.container.stop', label: 'Stop chadash', target: 'docker:i_d/engine', params: {}, tier: 'T2', confirm: 'hold', step_up: 'none', steps: [], impact, undo: null, expires_at: '2026-10-06T12:00:00Z' },
    } as never;
    await flushPromises();
    const note = document.querySelector('.confirm .confirm-note.is-self');
    expect(note?.textContent).toContain('Cha Dash itself down');
    const first = document.querySelector('.blast li');
    expect(first?.classList.contains('is-self')).toBe(true);
    expect(first?.textContent).toContain('Cha Dash itself');
    expect(await axeViolations()).toEqual([]);

    hs.ui.state.confirm = {
      kind: 'type',
      req: { action: 'proxmox.guest.reboot', target: 'proxmox:i_p/node/io', label: 'Reboot io' },
      action: { id: 'proxmox.guest.reboot', label: 'Reboot io', tier: 'T4', target: 'proxmox:i_p/node/io' },
      plan: { id: 'p_2', hash: 'sha256:y', action: 'proxmox.guest.reboot', label: 'Reboot io', target: 'proxmox:i_p/node/io', params: {}, tier: 'T4', confirm: 'type', confirm_text: 'io', step_up: 'fresh', steps: [], impact, undo: null, expires_at: '2026-10-06T12:00:00Z' },
    } as never;
    await flushPromises();
    expect(document.querySelector('.act-hub')?.textContent).toContain('Cha Dash itself down');
    expect(document.querySelector('.act-impact li.is-self')?.textContent).toContain('Cha Dash itself');
    expect(await axeViolations()).toEqual([]);
  });
});

describe('hub-cached icons', () => {
  it('keeps only same-origin icon paths, and finds a tile’s icon by its URL, its link, then its instance', async () => {
    const f = fakeFetch({
      'GET /api/icons': {
        status: 200,
        body: {
          by_icon: { 'https://cdn.example/sonarr.svg': '/api/icons/ic_abcdefgh2345?v=1a2b', bad: 'https://evil.example/x.png', worse: 'javascript:alert(1)' },
          by_origin: { 'https://photos.lab.example': '/api/icons/ic_photos234567?v=ff' },
          by_instance: { i_s: '/api/icons/ic_instance2345' },
        },
      },
    });
    await loadIcons();
    await loadIcons(); // once
    expect(f.calls.filter((c) => c.path === '/api/icons')).toHaveLength(1);
    const m = iconMap.value;
    expect(m.by_icon).toEqual({ 'https://cdn.example/sonarr.svg': '/api/icons/ic_abcdefgh2345?v=1a2b' });
    expect(iconFor(m, { icon: 'https://cdn.example/sonarr.svg', href: 'https://photos.lab.example' }, 'https://photos.lab.example')).toBe('/api/icons/ic_abcdefgh2345?v=1a2b');
    expect(iconFor(m, { icon: 'sonarr.png' }, 'https://photos.lab.example/albums')).toBe('/api/icons/ic_photos234567?v=ff');
    expect(iconFor(m, { bind: { instance: 'i_s' } }, undefined)).toBe('/api/icons/ic_instance2345');
    expect(iconFor(m, {}, 'https://other.lab.example')).toBeUndefined();
  });

  it('shows the icon in the tile, and the monogram when there’s none, it fails, or the tile needs you', async () => {
    fakeFetch({ 'GET /api/icons': { status: 200, body: { by_icon: {}, by_origin: { 'https://photos.lab.example': '/api/icons/ic_photos234567?v=ff' }, by_instance: {} } } });
    await loadIcons();
    const hs = harness();
    const Host = (status: TileStatus) =>
      defineComponent({
        setup() {
          provideTile({ itemId: 'service@x', type: 'service', config: { value: { name: 'Photos', href: 'https://photos.lab.example/' } } as never });
          return () => h('main', { 'aria-label': 'Front page' }, [h(Tile, { size: 'small', name: 'Photos', status })]);
        },
      });
    const w = hs.mountIn(Host('ok'));
    await nextTick();
    const img = document.querySelector<HTMLImageElement>('.mono-img');
    expect(img?.getAttribute('src')).toBe('/api/icons/ic_photos234567?v=ff');
    expect(img?.getAttribute('alt')).toBe('');
    expect(document.querySelector('a.mono')?.getAttribute('aria-label')).toBe('Open Photos');
    expect(await axeViolations()).toEqual([]);
    img!.dispatchEvent(new Event('error'));
    await nextTick();
    expect(document.querySelector('.mono-img')).toBeNull();
    expect(document.querySelector('a.mono')?.textContent?.trim()).toBe('PH');
    expect(document.querySelector('a.mono')?.getAttribute('aria-label')).toBe('PH, open Photos');
    w.unmount();
    document.body.innerHTML = '';
    hs.mountIn(Host('down'));
    await nextTick();
    expect(document.querySelector('.mono-img')).toBeNull();
  });
});

describe('Home file imports: security settings and decisions', () => {
  it('lists both, and says when the security settings weren’t imported', () => {
    const p = backupPreviewOf({
      plan: {
        changes: [
          {
            kind: 'security',
            id: 'security',
            name: 'Security',
            action: 'unchanged',
            skipped: 'Only the owner can change the security settings, so this file’s security settings weren’t imported.',
            fields: [{ path: '/posture', op: 'replace', from: 'relaxed', to: 'strict' }],
          },
          { kind: 'correlation', id: 'a:x/1 same_as b:y/2', name: 'a:x/1 same as b:y/2', action: 'create', detail: 'confirm' },
          { kind: 'correlation', id: 'c:z/3 runs_on d:w/4', name: 'c:z/3 runs on d:w/4', action: 'unchanged' },
        ],
      },
    });
    expect(p.sections.map((s) => [s.title, s.changed.length, s.unchanged.length])).toEqual([
      ['Security settings', 1, 0],
      ['Same-thing decisions', 1, 1],
    ]);
    expect(p.sections[0]!.changed[0]!.detail).toContain('Not imported: Only the owner');
    expect(p.sections[0]!.changed[0]!.fields).toEqual([{ path: 'posture', op: 'replace', from: '"relaxed"', to: '"strict"' }]);
  });
});
