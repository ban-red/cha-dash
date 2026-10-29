import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import DiscoverPane from '../src/discover/DiscoverPane.vue';
import LeadsItem from '../src/components/LeadsItem.vue';
import { leadsJSON, leadsOf } from '../src/model/leads';
import type { GraphDoc, Resource } from '../src/model/types';
import { route, syncRoute } from '../src/lib/router';
import { resetIntegrations } from '../src/stores/integrations';
import { resetLeads } from '../src/stores/leads';
import { fakeFetch } from './fake-fetch';
import { harness } from './harness';

const container = (name: string, image: string, extra: Partial<Resource> = {}): Resource => ({
  id: `docker:i_d/container/${name}`,
  kind: 'container',
  name,
  attrs: { image, published: ['8989->8989/tcp'] },
  ...extra,
});
const graph = (...rs: Resource[]): GraphDoc => ({ resources: Object.fromEntries(rs.map((r) => [r.id, r])), relations: {} });
const docker = { id: 'i_d', integration: 'docker', config: { endpoint: 'tcp://nas.lab.example:2375' } };

beforeEach(() => {
  history.replaceState(null, '', '/settings/import/discover');
  syncRoute();
  resetIntegrations();
  resetLeads();
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

describe('leads from the graph', () => {
  it('finds a known container at its published port on the Docker host', () => {
    const leads = leadsOf(graph(container('sonarr', 'lscr.io/linuxserver/sonarr:latest')), [docker]);
    expect(leads).toEqual([
      { key: 'sonarr|http://nas.lab.example:8989', integration: 'sonarr', name: 'Sonarr', url: 'http://nas.lab.example:8989', ways: [{ way: 'container', text: 'container sonarr · :8989', resource: 'docker:i_d/container/sonarr' }] },
    ]);
  });

  it('merges a container and its route into one lead that prefers the route', () => {
    const route: Resource = { id: 'traefik:i_t/route/sonarr', kind: 'route', name: 'sonarr', attrs: { url: 'https://sonarr.home.example/' } };
    const leads = leadsOf(graph(container('sonarr', 'ghcr.io/hotio/sonarr', { attrs: { image: 'ghcr.io/hotio/sonarr' } }), route), [docker]);
    expect(leads).toHaveLength(1);
    expect(leads[0]).toMatchObject({ url: 'https://sonarr.home.example', href: 'https://sonarr.home.example' });
    expect(leads[0]!.ways.map((w) => w.way)).toEqual(['route', 'container']);
  });

  it('uses the homepage.href label and name, and skips what’s read or dismissed', () => {
    const labelled = container('plex', 'plexinc/pms-docker', { labels: { 'homepage.href': 'https://plex.home.example', 'homepage.name': 'Plex (living room)' } });
    const g = graph(labelled, container('sonarr', 'sonarr'));
    expect(leadsOf(g, [docker]).map((l) => l.name)).toEqual(['Plex (living room)', 'Sonarr']);
    const read = [docker, { id: 'i_p', integration: 'plex', config: { url: 'https://PLEX.home.example/' } }];
    expect(leadsOf(g, read).map((l) => l.integration)).toEqual(['sonarr']);
    expect(leadsOf(g, [docker], new Set(['sonarr|http://nas.lab.example:8989'])).map((l) => l.integration)).toEqual(['plex']);
  });

  it('a lead without an address is still offered, keyed by its resource; unknown images aren’t', () => {
    const leads = leadsOf(graph(container('radarr', 'radarr'), container('nginx', 'nginx')), []);
    expect(leads.map((l) => [l.key, l.url])).toEqual([['radarr|docker:i_d/container/radarr', undefined]]);
  });

  it('writes what the discovery import takes', () => {
    const leads = leadsOf(graph(container('sonarr', 'sonarr')), [docker]);
    expect(JSON.parse(leadsJSON(leads))).toEqual([{ integration: 'sonarr', name: 'Sonarr', url: 'http://nas.lab.example:8989' }]);
  });
});

describe('Settings → Bring in → Discover', () => {
  function mount(over: Record<string, unknown> = {}) {
    const f = fakeFetch({
      'GET /api/integrations/instances': { status: 200, body: [{ ...docker, name: 'Docker', status: 'ok' }] },
      'GET /api/discover/dismissed': { status: 200, body: { dismissed: [] } },
      'POST /api/onboarding/discover': { status: 200, body: { found: [], checked: [] } },
      'POST /api/discover/dismiss': { status: 200, body: { dismissed: ['sonarr|http://nas.lab.example:8989'] } },
      'POST /api/discover/restore': { status: 200, body: { dismissed: [] } },
      'POST /api/import/discovery?dry_run=1': {
        status: 200,
        body: { plan: { source: 'discovery', board: { name: 'Discovered', layout: { sections: [] } }, tiles: [], instances: [{ id: 'i_s', integration: 'sonarr', name: 'Sonarr', config: { url: 'http://nas.lab.example:8989' }, missing_secrets: ['api_key'] }], changes: [] }, hints: [], unmapped: [], plan_hash: 'sha256:x' },
      },
      ...over,
    });
    const h = harness({ graph: graph(container('sonarr', 'sonarr'), container('radarr', 'radarr', { id: 'docker:i_x/container/radarr' })) });
    const w = h.mountIn(DiscoverPane);
    return { ...h, w, f };
  }

  it('lists leads, checks only the picked addresses, and reviews them as an import', async () => {
    const { w, f } = mount({
      'POST /api/onboarding/discover': (body: unknown) => {
        expect(body).toEqual({ urls: [{ integration: 'sonarr', url: 'http://nas.lab.example:8989' }] });
        return { status: 200, body: { found: [{ integration: 'sonarr', name: 'Sonarr', config: { url: 'http://nas.lab.example:8989' }, source: 'lead', target: 'x', ready: false, grants: [], warnings: [] }], checked: [{ target: 'http://nas.lab.example:8989', source: 'lead', found: 1 }] } };
      },
    });
    await flushPromises();
    const rows = w.findAll('.leads li');
    expect(rows.map((r) => r.find('.imp-name').text())).toEqual(['Radarr · no address seen yet', 'Sonarr · http://nas.lab.example:8989']);
    await w.find('input[type="checkbox"]').setValue(true);
    await w.findAll('button').find((b) => b.text() === 'Check 1')!.trigger('click');
    await flushPromises();
    expect(w.text()).toContain('answers as Sonarr');
    await w.findAll('button').find((b) => b.text() === 'Review 1 selected')!.trigger('click');
    await flushPromises();
    expect(f.last('POST', '/api/import/discovery?dry_run=1')!.body).toEqual({ files: { leads_json: '[{"integration":"sonarr","name":"Sonarr","url":"http://nas.lab.example:8989"}]' }, names: {} });
    expect(w.find('.imp-review').text()).toContain('Needs api_key');
    await w.findAll('.imp-apply button').find((b) => b.text() === 'Cancel')!.trigger('click');
    expect(w.find('.leads').exists()).toBe(true);
  });

  it('dismisses a lead hub-wide, with Undo, and offers dismissed ones again', async () => {
    const { w, f, ui } = mount();
    await flushPromises();
    await w.findAll('.leads li')[1]!.findAll('button').find((b) => b.text() === 'Dismiss')!.trigger('click');
    await flushPromises();
    expect(f.last('POST', '/api/discover/dismiss')!.body).toEqual({ keys: ['sonarr|http://nas.lab.example:8989'] });
    expect(w.findAll('.leads li')).toHaveLength(1);
    await w.findAll('button').find((b) => b.text() === 'Dismissed · 1')!.trigger('click');
    expect(w.text()).toContain('Offer again');
    ui.state.toasts[0]!.action!.run();
    await flushPromises();
    expect(f.last('POST', '/api/discover/restore')!.body).toEqual({ keys: ['sonarr|http://nas.lab.example:8989'] });
    expect(w.findAll('.leads li')).toHaveLength(2);
  });
});

describe('the Inbox’s leads item', () => {
  it('reviews in Settings, and dismisses them all', async () => {
    const f = fakeFetch({ 'POST /api/discover/dismiss': { status: 200, body: { dismissed: ['a', 'b'] } } });
    const h = harness();
    const leads = leadsOf(graph(container('sonarr', 'sonarr'), container('plex', 'plex')), [docker]);
    const w = h.mountIn(LeadsItem, { leads });
    expect(w.find('.ib-title').text()).toBe('2 services found through what’s connected. Review?');
    await w.findAll('button').find((b) => b.text() === 'Dismiss')!.trigger('click');
    await flushPromises();
    expect((f.last('POST', '/api/discover/dismiss')!.body as { keys: string[] }).keys).toHaveLength(2);
    await w.findAll('button').find((b) => b.text() === 'Review')!.trigger('click');
    expect(route.value.path).toBe('/settings/import/discover');
  });
});
