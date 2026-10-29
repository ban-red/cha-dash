/**
 * The hub's correlator (internal/correlate) keeps one resource per source and
 * links them with `same_as`; lists show one of each thing, and its
 * questions arrive in the Inbox as `suggestion` items.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { nextTick } from 'vue';
import { aliases, byKind, distinct, engines, members, mergedHealth, sourcesOf } from '../src/model/graph';
import { groupInbox, incidentsFor } from '../src/model/inbox';
import type { GraphDoc, Incident, InboxDoc, MetricsDoc, Resource } from '../src/model/types';
import ContainersWidget from '../src/widgets/ContainersWidget.vue';
import InboxSheet from '../src/components/InboxSheet.vue';
import { fakeFetch } from './fake-fetch';
import { harness } from './harness';

afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

const res = (id: string, kind: string, name: string, extra: Partial<Resource> = {}): Resource => ({
  id,
  kind,
  name,
  state: 'running',
  health: 'ok',
  source: `integration:${id.split(':')[1]!.split('/')[0]}`,
  ...extra,
});

const suggestion: Incident = {
  id: 'correlate.0123456789abcdef',
  scenario: 'merge-suggestion',
  class: 'suggestion',
  severity: 'info',
  status: 'open',
  phase: 'open',
  title: 'These look like the same thing. Merge?',
  summary: 'Sonarr (Sonarr) and the container sonarr (Magpie Docker): Sonarr and the container sonarr have matching names.',
  target: 'sonarr:i_s/app',
  opened_at: '2026-10-03T14:00:00Z',
  facts: { from: 'sonarr:i_s/app', to: 'docker:i_d/container/sonarr', relation: 'runs_on', rule: 'name', confidence: 'weak' },
  causes: [],
  blast_radius: [],
  suggested: { id: 'correlate.confirm', label: 'Merge', tier: 'T0', target: 'sonarr:i_s/app' },
  alternatives: [{ id: 'correlate.reject', label: 'Keep separate', tier: 'T0', target: 'sonarr:i_s/app' }],
  resolves_when: 'Resolves when you merge them or keep them separate.',
};

/** magpie: Docker and Unraid both report its containers; the correlator merged them. */
function home(): { graph: GraphDoc; metrics: MetricsDoc; inbox: InboxDoc } {
  const resources: Record<string, Resource> = {};
  for (const r of [
    res('integration:i_d', 'integration', 'Magpie Docker', { source: 'hub' }),
    res('integration:i_u', 'integration', 'Magpie', { source: 'hub' }),
    res('docker:i_d/engine', 'engine', 'magpie'),
    res('unraid:i_u/host', 'host', 'magpie', { attrs: { platform: 'unraid' } }),
    // Docker: exited, restart policy "no", so fine as far as Docker knows.
    res('docker:i_d/container/sonarr', 'container', 'sonarr', { state: 'exited', attrs: { 'health.reason': 'stopped (exited 0)' } }),
    // Unraid: it's on the autostart list, so it should run.
    res('unraid:i_u/container/sonarr', 'container', 'sonarr', { state: 'exited', health: 'attention', attrs: { 'health.reason': 'on the autostart list, but not running' } }),
    res('docker:i_d/container/radarr', 'container', 'radarr'),
    res('unraid:i_u/container/radarr', 'container', 'radarr'),
    res('docker:i_d/container/plex', 'container', 'plex'),
    res('sonarr:i_s/app', 'app', 'Sonarr', { attrs: { type: 'sonarr' } }),
  ]) {
    resources[r.id] = r;
  }
  const relations: GraphDoc['relations'] = {};
  const rel = (from: string, type: string, to: string, source: string) => {
    relations[`${from} ${type} ${to}`] = { from, type, to, source };
  };
  for (const c of ['sonarr', 'radarr', 'plex']) rel(`docker:i_d/container/${c}`, 'runs_on', 'docker:i_d/engine', 'integration:i_d');
  for (const c of ['sonarr', 'radarr']) {
    rel(`unraid:i_u/container/${c}`, 'runs_on', 'unraid:i_u/host', 'integration:i_u');
    rel(`unraid:i_u/container/${c}`, 'same_as', `docker:i_d/container/${c}`, 'correlator');
  }
  rel('docker:i_d/engine', 'runs_on', 'unraid:i_u/host', 'correlator');
  return {
    graph: { resources, relations },
    metrics: { series: {} },
    inbox: { now: '2026-10-03T14:05:00Z', incidents: { [suggestion.id]: structuredClone(suggestion) } },
  };
}

describe('one of each thing', () => {
  it('skips aliases in lists, and merges what every source says', () => {
    const { graph } = home();
    expect([...aliases(graph)].sort()).toEqual(['unraid:i_u/container/radarr', 'unraid:i_u/container/sonarr']);
    expect(byKind(graph, 'container').map((r) => r.id)).toEqual(['docker:i_d/container/plex', 'docker:i_d/container/radarr', 'docker:i_d/container/sonarr']);
    expect(distinct(graph).some((r) => r.id.startsWith('unraid:i_u/container/'))).toBe(false);
    expect(members(graph, 'unraid:i_u/container/sonarr').map((r) => r.id)).toEqual(['docker:i_d/container/sonarr', 'unraid:i_u/container/sonarr']);
    expect(sourcesOf(graph, 'docker:i_d/container/sonarr')).toEqual(['Magpie Docker', 'Magpie']);
    expect(sourcesOf(graph, 'docker:i_d/container/plex')).toEqual(['Magpie Docker']);
    // The worst health of its sources, with that source's reason.
    expect(mergedHealth(graph, 'docker:i_d/container/sonarr')).toEqual({ status: 'attention', reason: 'on the autostart list, but not running' });
  });

  it('lists an engine’s containers once, with both sources', () => {
    const [e] = engines(home().graph);
    expect(e!.total).toBe(3);
    expect(e!.running).toBe(2);
    expect(e!.unhealthy.map((c) => [c.name, c.health.status, c.sources])).toEqual([['sonarr', 'attention', ['Magpie Docker', 'Magpie']]]);
  });

  it('shows one row per container in the containers tile', async () => {
    const { mountIn } = harness(home());
    await flushPromises();
    const w = mountIn(ContainersWidget, { size: 'tall' });
    await nextTick();
    expect(w.text()).toContain('2/3 running');
    const row = w.find('.list .li');
    expect(row.text()).toContain('sonarr');
    expect(row.text()).toContain('on the autostart list, but not running');
    expect(row.find('.src').text()).toBe('Magpie Docker · Magpie');
    // Problems first, then the rest by name: each container once.
    expect(w.findAll('.list .li .t').map((s) => s.text())).toEqual(['sonarr', 'plex', 'radarr']);
    const large = mountIn(ContainersWidget, { size: 'large' });
    await nextTick();
    expect(large.text()).toContain('1 not healthy');
    expect(large.findAll('.sr:not(.head)')).toHaveLength(3);
    expect(large.find('.sr:not(.head) .src').text()).toBe('Magpie Docker · Magpie');
  });
});

describe('suggestions in the Inbox', () => {
  it('are their own group: not problems, not incidents on a tile', () => {
    const { inbox } = home();
    const g = groupInbox(inbox);
    expect(g.suggestions.map((i) => i.id)).toEqual([suggestion.id]);
    expect(g.open).toEqual([]);
    expect(g.fixing).toEqual([]);
    expect(incidentsFor(inbox, ['sonarr:i_s/app'])).toEqual([]);
  });

  it('ask, and send the answer to the hub', async () => {
    const api = fakeFetch({
      [`POST /api/correlate/suggestions/${suggestion.id}/confirm`]: { status: 200, body: { id: 'cd_1', verdict: 'confirm' } },
      [`POST /api/correlate/suggestions/${suggestion.id}/reject`]: { status: 404, body: { error: { code: 'not_found', message: 'no such suggestion' } } },
    });
    const h = harness(home());
    h.mountIn(InboxSheet);
    h.ui.openInbox();
    await flushPromises();
    await nextTick();
    const panel = document.body.querySelector<HTMLElement>('.inbox')!;
    expect([...panel.querySelectorAll('.ib-grp')].map((g) => g.textContent?.trim())).toEqual(['Suggestions · 1']);
    expect(panel.querySelector('.ib-head .n')!.textContent).toBe('1 needs you');
    const item = panel.querySelector<HTMLElement>('.sugg')!;
    expect(item.querySelector('.ib-title')!.textContent).toBe('These look like the same thing. Merge?');
    expect(item.textContent).toContain('have matching names');
    const [merge, keep] = [...item.querySelectorAll<HTMLButtonElement>('button')];
    expect([merge!.textContent?.trim(), keep!.textContent?.trim()]).toEqual(['Merge', 'Keep separate']);

    merge!.click();
    await flushPromises();
    expect(api.last('POST', `/api/correlate/suggestions/${suggestion.id}/confirm`)).toBeDefined();
    expect(h.ui.state.toasts.at(-1)?.text).toBe('Merged. Cha Dash shows them as one.');

    keep!.click();
    await flushPromises();
    expect(h.ui.state.toasts.at(-1)).toMatchObject({ tone: 'error', text: 'That was already answered, or no longer applies.' });
  });
});
