import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { nextTick } from 'vue';
import { toDocument } from '@chadash/layout';
import OnboardingWizard from '../src/onboarding/OnboardingWizard.vue';
import { cache, parseHosts, validHost, type Candidate } from '../src/onboarding/discover';
import { takeDraft } from '../src/onboarding/draft';
import { closeWizard, loadOnboarding, onboarding, openWizard, resetOnboarding } from '../src/onboarding/state';
import { route, syncRoute } from '../src/lib/router';
import { defaultBoard } from '../src/model/defaultLayout';
import type { GraphDoc, Resource } from '../src/model/types';
import { resetIntegrations } from '../src/stores/integrations';
import { fakeFetch, type Handler } from './fake-fetch';
import { harness } from './harness';

const mounted: { unmount: () => void }[] = [];

const RECIPE = (access: string) => ({ title: 'T', summary: 'S', access, params: [], steps: [{ title: 'Step', blocks: [{ kind: 'text', text: 'x' }] }], notes: [], fills: {}, grants: [] });
const CATALOG = [
  { id: 'docker', name: 'Docker', config_schema: { type: 'object', properties: { endpoint: { type: 'string' } } }, recipe: RECIPE('read-only') },
  { id: 'proxmox', name: 'Proxmox VE', config_schema: { type: 'object', properties: { url: { type: 'string' } } }, recipe: RECIPE('read-only') },
  { id: 'sonarr', name: 'Sonarr', config_schema: { type: 'object', properties: { url: { type: 'string' } } }, recipe: RECIPE('full') },
  { id: 'zzz', name: 'Zed', config_schema: { type: 'object', properties: {} } },
];

const SIDECAR: Candidate = {
  integration: 'docker',
  name: 'Docker',
  config: { endpoint: 'tcp://socket-proxy:2375' },
  source: 'sidecar',
  target: 'tcp://socket-proxy:2375',
  ready: true,
  version: '28.5.1',
  grants: [{ name: 'containers', access: 'read' }],
  warnings: [],
};

function hub(over: Record<string, Handler | { status: number; body?: unknown }> = {}) {
  const instances: unknown[] = [];
  return fakeFetch({
    'GET /api/onboarding': { status: 200, body: { status: 'new', step: '', show: true, sidecar: 'tcp://socket-proxy:2375', updated_at: null } },
    'PUT /api/onboarding': (body) => ({ status: 200, body: { ...(body as object), show: false } }),
    'GET /api/integrations/catalog': { status: 200, body: CATALOG },
    'GET /api/integrations/instances': () => ({ status: 200, body: instances }),
    'POST /api/integrations/instances': (body) => {
      const b = body as { integration: string; name: string; config: unknown };
      instances.push({ id: 'i_d', integration: b.integration, name: b.name, status: 'ok', warnings: [], config: b.config });
      return { status: 201, body: { id: 'i_d' } };
    },
    'POST /api/onboarding/discover': (body) => {
      const b = body as { sidecar?: boolean; hosts?: string[]; docker?: string[] };
      if (b.sidecar) return { status: 200, body: { found: [SIDECAR], checked: [{ target: SIDECAR.target, source: 'sidecar', found: 1 }] } };
      return {
        status: 200,
        body: {
          found: [
            {
              integration: 'proxmox',
              name: 'Proxmox VE',
              config: { url: 'https://nas.home.example:8006', tls: { mode: 'tofu' } },
              source: 'host',
              target: 'nas.home.example',
              ready: false,
              grants: [],
              warnings: [],
            },
            {
              ...SIDECAR,
              name: 'Docker on dockerproxy',
              config: { endpoint: 'tcp://dockerproxy:2375' },
              source: 'docker',
              target: 'tcp://dockerproxy:2375',
              warnings: [{ code: 'docker_broad_proxy', severity: 'warning', message: 'This proxy lets every container’s environment through.' }],
            },
          ],
          checked: [
            { target: 'tcp://dockerproxy:2375', source: 'docker', found: 1 },
            { target: 'nas.home.example', source: 'host', found: 1 },
            { target: 'gone.home.example', source: 'host', found: 0 },
          ],
        },
      };
    },
    ...over,
  });
}

const button = (text: string) => [...document.body.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim().startsWith(text));
const heading = () => document.getElementById('ob-h')?.textContent;

beforeEach(() => {
  history.replaceState(null, '', '/');
  syncRoute();
  resetIntegrations();
  resetOnboarding();
  delete cache.sidecar;
});
afterEach(() => {
  for (const w of mounted.splice(0)) w.unmount();
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

describe('the wizard’s state', () => {
  it('opens on a new hub, once, and remembers the step on the hub', async () => {
    const f = hub();
    await loadOnboarding();
    expect(onboarding.open).toBe(true);
    expect(onboarding.step).toBe('start');
    expect(f.last('PUT', '/api/onboarding')!.body).toEqual({ status: 'started', step: 'start' });

    await closeWizard('skipped');
    expect(onboarding.open).toBe(false);
    expect(f.last('PUT', '/api/onboarding')!.body).toEqual({ status: 'skipped' });
  });

  it('resumes where it was left, and stays shut when the hub says so', async () => {
    hub({ 'GET /api/onboarding': { status: 200, body: { status: 'started', step: 'discover', show: true, sidecar: 'tcp://socket-proxy:2375' } } });
    await loadOnboarding();
    expect(onboarding.open && onboarding.step).toBe('discover');

    resetOnboarding();
    hub({ 'GET /api/onboarding': { status: 200, body: { status: 'done', step: '', show: false, sidecar: '' } } });
    await loadOnboarding();
    expect(onboarding.open).toBe(false);
    // Opened again by hand (Settings → Integrations → Setup guide), from the start.
    openWizard();
    expect(onboarding.open && onboarding.step).toBe('start');

    resetOnboarding();
    fakeFetch({}); // an older hub without the route
    await loadOnboarding();
    expect(onboarding.open).toBe(false);
  });

  it('checks machines the way the hub does', () => {
    expect(validHost('nas.home.example')).toBe(true);
    expect(validHost('192.0.2.20')).toBe(true);
    expect(validHost('2001:db8::1')).toBe(true);
    for (const bad of ['198.51.100.0/24', '*.home.example', 'http://nas', 'nas:8006', '192.0.2.300', '198.51.100.1-50', '2375']) expect(validHost(bad), bad).toBe(false);
    expect(parseHosts(' nas.home.example\n192.0.2.20, nas.home.example  203.0.113.0/24')).toEqual({ hosts: ['nas.home.example', '192.0.2.20'], bad: ['203.0.113.0/24'] });
  });
});

async function wizard(step: Parameters<typeof openWizard>[0] = 'start', graph?: GraphDoc) {
  const h = harness(graph ? { graph } : undefined);
  openWizard(step);
  mounted.push(h.mountIn(OnboardingWizard));
  await flushPromises();
  await nextTick();
  return h;
}

describe('Connect your home', () => {
  it('offers three ways in, and can be skipped', async () => {
    const f = hub();
    await wizard();
    expect(heading()).toBe('Connect your home.');
    expect([...document.querySelectorAll('.ob-path-t')].map((p) => p.textContent)).toEqual(['Import a config', 'Discover', 'Add one by one']);
    expect(document.activeElement).toBe(document.getElementById('ob-h'));
    button('Skip setup')!.click();
    await flushPromises();
    expect(onboarding.open).toBe(false);
    expect(f.last('PUT', '/api/onboarding')!.body).toEqual({ status: 'skipped' });
  });

  it('closes to finish later with Esc, keeping its step', async () => {
    const f = hub();
    await wizard('add');
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    await flushPromises();
    expect(onboarding.open).toBe(false);
    expect(f.last('PUT', '/api/onboarding')!.body).toEqual({ status: 'started', step: 'add' });
  });

  it('discovers Docker through the sidecar and adds it in one click', async () => {
    const f = hub();
    await wizard();
    (document.querySelectorAll<HTMLButtonElement>('.ob-path')[1]!).click();
    await flushPromises();
    expect(heading()).toBe('Discover what’s here');
    expect(f.last('PUT', '/api/onboarding')!.body).toEqual({ status: 'started', step: 'discover' });
    // The sidecar is the only thing probed without asking.
    expect(f.calls.filter((c) => c.path === '/api/onboarding/discover').map((c) => c.body)).toEqual([{ sidecar: true }]);
    const card = document.querySelector('.ob-cand')!;
    expect(card.textContent).toContain('Docker on this host');
    expect(card.textContent).toContain('28.5.1');

    (card.querySelector('button') as HTMLButtonElement).click();
    await flushPromises();
    expect(f.last('POST', '/api/integrations/instances')!.body).toEqual({ integration: 'docker', name: 'Docker', config: { endpoint: 'tcp://socket-proxy:2375' }, secrets: {} });
    expect(document.querySelector('.ob-cand')!.textContent).toContain('Added');
    expect(document.querySelector('.ob-connected')!.textContent).toContain('Docker');
    expect(document.querySelector('.ob-connected')!.textContent).toContain('Reading');

    // Back to this step later (the answer is remembered, not asked again): still added.
    for (const w of mounted.splice(0)) w.unmount();
    await wizard('discover');
    expect(f.calls.filter((c) => c.path === '/api/onboarding/discover')).toHaveLength(1);
    expect(document.querySelector('.ob-cand')!.textContent).toContain('Added');
    expect(document.querySelector('.ob-cand button')).toBeNull();
  });

  it('looks further only where it’s told to, and never at a subnet', async () => {
    const f = hub();
    await wizard('discover');
    const hosts = document.getElementById('ob-hosts') as HTMLTextAreaElement;
    hosts.value = 'nas.home.example\n198.51.100.0/24';
    hosts.dispatchEvent(new Event('input'));
    button('Check these')!.click();
    await flushPromises();
    expect(document.querySelector('.ob-discover .form-error')!.textContent).toContain('“198.51.100.0/24” isn’t one machine');
    expect(f.calls.filter((c) => c.path === '/api/onboarding/discover')).toHaveLength(1); // the sidecar only

    hosts.value = 'nas.home.example gone.home.example';
    hosts.dispatchEvent(new Event('input'));
    const box = [...document.querySelectorAll<HTMLInputElement>('.ob-check input')][0]!;
    box.click();
    await nextTick();
    button('Check these')!.click();
    await flushPromises();
    expect(f.last('POST', '/api/onboarding/discover')!.body).toEqual({ docker: ['tcp://dockerproxy:2375'], hosts: ['nas.home.example', 'gone.home.example'] });
    const found = [...document.querySelectorAll('ul[aria-label="Found"] .ob-cand')];
    expect(found.map((c) => c.querySelector('b')!.textContent)).toEqual(['Proxmox VE', 'Docker on dockerproxy']);
    expect(found[1]!.textContent).toContain('every container’s environment');
    expect(document.querySelector('.ob-misses')!.textContent).toContain('gone.home.example');

    // Something that needs a credential opens its add form, its address filled in.
    (found[0]!.querySelector('button') as HTMLButtonElement).click();
    expect(route.value.path).toBe('/settings/integrations/new/proxmox');
    expect(takeDraft('proxmox')).toEqual({ name: 'Proxmox VE', config: { url: 'https://nas.home.example:8006', tls: { mode: 'tofu' } } });
  });

  it('lists what it can add, grouped, with the recipe’s honest label', async () => {
    hub();
    await wizard('add');
    const groups = [...document.querySelectorAll('.ob-add .ob-block')].map((g) => g.querySelector('h3')!.textContent);
    expect(groups).toEqual(['Servers and network', 'Media and downloads', 'More']);
    const items = [...document.querySelectorAll('.ob-item')].map((i) => i.textContent?.replace(/\s+/g, ' ').trim());
    expect(items[0]).toContain('Proxmox VE');
    expect(items[0]).toContain('Read-only');
    expect(items.find((i) => i?.includes('Sonarr'))).toContain('Full access only');
    (document.querySelector('.ob-item') as HTMLAnchorElement).click();
    expect(route.value.path).toBe('/settings/integrations/new/proxmox');
    // Settings opens over the wizard, which waits underneath.
    await nextTick();
    expect(document.querySelector('.layer-onboarding')!.hasAttribute('inert')).toBe(true);
  });

  it('imports inside the wizard and finishes when the import lands on its board', async () => {
    hub({
      'POST /api/import/homepage?dry_run=1': { status: 200, body: { plan: { source: 'homepage', board: { name: 'Home', layout: { sections: [] } }, tiles: [], instances: [{ id: 'i1', integration: 'plex', name: 'Plex', config: {} }], changes: [] }, hints: [], unmapped: [], plan_hash: 'sha256:x' } },
      'POST /api/import/homepage': { status: 200, body: { import_id: 'imp_1', board_id: 'b_new' } },
    });
    await wizard();
    (document.querySelectorAll<HTMLButtonElement>('.ob-path')[0]!).click();
    await vi.dynamicImportSettled();
    await flushPromises();
    expect(route.value.path).not.toBe('/settings/import'); // it stays in the wizard's layer
    expect(onboarding.step).toBe('import');
    const services = document.querySelector<HTMLTextAreaElement>('#imp-services_yaml')!;
    services.value = '- Media:\n    - Plex:\n        href: https://plex.example\n';
    services.dispatchEvent(new Event('input'));
    [...document.querySelectorAll('button')].find((b) => b.textContent === 'Preview import')!.click();
    await flushPromises();
    [...document.querySelectorAll<HTMLButtonElement>('.imp-apply button')][0]!.click();
    await flushPromises();
    expect(route.value.path).toBe('/boards/b_new');
    expect(heading()).toBe('Your page is filling in.');
  });

  it('ends on a populated front page: an untouched board is laid out again', async () => {
    const engine: Resource = { id: 'docker:i_d/engine', kind: 'engine', name: 'Docker', state: 'running', health: 'ok' };
    const graph: GraphDoc = { resources: { [engine.id]: engine }, relations: {} };
    // Laid out when the hub had nothing: only the incidents tile.
    const empty = toDocument(defaultBoard({ resources: {}, relations: {} }));
    const f = hub({
      'GET /api/boards': { status: 200, body: { boards: [{ id: 'b1', name: 'Home' }] } },
      'GET /api/boards/b1/layout': { status: 200, body: { revision: 3, format_version: 1, layout: empty } },
      'PUT /api/boards/b1/layout': { status: 200, body: { revision: 4 } },
    });
    const h = await wizard('done', graph);
    await h.board.load();
    expect(heading()).toBe('Your page is filling in.');
    // The last step says where the security level is, and that it starts at Relaxed.
    expect(document.querySelector('.ob-posture')!.textContent).toMatch(/starts at Relaxed.*Settings → Security/s);
    button('Open the front page')!.click();
    await flushPromises();
    const put = f.last('PUT', '/api/boards/b1/layout')!.body as { revision: number; layout: { sections: { items: { id: string }[] }[] } };
    expect(put.revision).toBe(3);
    expect(put.layout.sections.flatMap((s) => s.items.map((i) => i.id))).toEqual(expect.arrayContaining(['containers', 'incidents']));
    expect(f.last('PUT', '/api/onboarding')!.body).toEqual({ status: 'done' });
    expect(onboarding.open).toBe(false);
  });

  it('leaves a board someone arranged alone', async () => {
    const graph: GraphDoc = { resources: { 'docker:i_d/engine': { id: 'docker:i_d/engine', kind: 'engine', name: 'Docker', state: 'running', health: 'ok' } }, relations: {} };
    const host: Resource = { id: 'unraid:i_u/host', kind: 'host', name: 'nas', attrs: { platform: 'unraid' } };
    const arranged = toDocument(defaultBoard({ resources: { [host.id]: host }, relations: {} }));
    const f = hub({
      'GET /api/boards': { status: 200, body: { boards: [{ id: 'b1', name: 'Home' }] } },
      'GET /api/boards/b1/layout': { status: 200, body: { revision: 7, format_version: 1, layout: arranged } },
    });
    const h = await wizard('done', graph);
    await h.board.load();
    button('Open the front page')!.click();
    await flushPromises();
    expect(f.calls.some((c) => c.method === 'PUT' && c.path.startsWith('/api/boards/'))).toBe(false);
    expect(onboarding.open).toBe(false);
  });
});
