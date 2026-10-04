/**
 * Widgets on real integration instances, not the Demo Home: resources are
 * `<integration>:<instance id>/…` (07 · Wave 5 conventions), so widgets find
 * their app by `attrs.type` and its integration through `monitors`.
 */
import { describe, expect, it } from 'vitest';
import { appsOfType, integrationOf, resolve } from '../src/model/graph';
import { defaultBoard } from '../src/model/defaultLayout';
import type { GraphDoc, InboxDoc, MetricsDoc, Resource } from '../src/model/types';
import PlexWidget from '../src/widgets/PlexWidget.vue';
import ArrWidget from '../src/widgets/ArrWidget.vue';
import { harness } from './harness';

const app = (id: string, type: string, name: string): Resource => ({ id, kind: 'app', name, state: 'running', health: 'ok', attrs: { type } });

function home(): { graph: GraphDoc; metrics: MetricsDoc; inbox: InboxDoc } {
  const resources: Record<string, Resource> = {};
  for (const r of [
    app('plex:i_p/app', 'plex', 'Plex'),
    app('sonarr:i_s2/app', 'sonarr', 'Sonarr 4K'),
    app('sonarr:i_s1/app', 'sonarr', 'Sonarr'),
    { id: 'integration:i_p', kind: 'integration', name: 'Plex', health: 'ok' },
  ]) {
    resources[r.id] = r;
  }
  return {
    graph: { resources, relations: { a: { from: 'integration:i_p', type: 'monitors', to: 'plex:i_p/app' } } },
    metrics: {
      series: {
        'plex:i_p/app#streams': { value: 2, as_of: '2026-10-03T14:05:00Z' },
        'sonarr:i_s1/app#queue': { value: 3, as_of: '2026-10-03T14:05:00Z' },
      },
    },
    inbox: { now: '2026-10-03T14:05:00Z', incidents: {} },
  };
}

describe('resolving widgets to real instances', () => {
  it('prefers the exact ID, then the app of that type', () => {
    const { graph } = home();
    expect(resolve(graph, 'plex:i_p/app')?.name).toBe('Plex');
    expect(resolve(graph, 'app:plex')?.id).toBe('plex:i_p/app');
    expect(resolve(graph, 'app:sonarr')?.id).toBe('sonarr:i_s1/app'); // the first by ID
    expect(resolve(graph, 'app:radarr')).toBeUndefined();
    expect(appsOfType(graph, 'sonarr').map((r) => r.id)).toEqual(['sonarr:i_s1/app', 'sonarr:i_s2/app']);
    expect(integrationOf(graph, 'plex:i_p/app')?.id).toBe('integration:i_p');
  });

  it('builds the default board with one queue tile per Sonarr instance', () => {
    const items = defaultBoard(home().graph).sections.flatMap((s) => s.items.map((i) => i.id));
    expect(items).toContain('plex');
    expect(items).toContain('arr@sonarr:i_s1/app');
    expect(items).toContain('arr@sonarr:i_s2/app');
  });

  it('shows a real Plex instance, and its repair', async () => {
    const data = home();
    const { mountIn, ws } = harness(data);
    const w = mountIn(PlexWidget, { size: 'small' });
    expect(w.text()).toContain('2playing');
    expect(w.text()).not.toContain('Token rejected');

    data.inbox.incidents['i_p.repair'] = {
      id: 'i_p.repair',
      class: 'repair',
      severity: 'attention',
      status: 'open',
      title: 'Plex rejected the token',
      summary: '',
      target: 'integration:i_p',
      opened_at: '2026-10-03T14:05:00Z',
    };
    ws.handshake(data);
    await w.vm.$nextTick();
    expect(w.text()).toContain('Token rejected');
  });

  it('shows a real Sonarr queue from its layout item', () => {
    const { mountIn } = harness(home());
    const w = mountIn(ArrWidget, { size: 'small', target: 'sonarr:i_s1/app' });
    expect(w.find('.tile').attributes('aria-label')).toContain('Sonarr');
    expect(w.text()).toContain('3');
  });
});
