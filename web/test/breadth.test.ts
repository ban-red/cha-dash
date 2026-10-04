/**
 * v0.2's breadth integrations on the existing widgets: the service tile's
 * stat per type, the Queue and Up next tiles reading Lidarr, and the
 * Downloads tile reading Transmission and NZBGet with qBittorrent's and
 * SABnzbd's keys. Shapes are the ones each integration's README documents.
 */
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import WidgetHost from '../src/widgets/WidgetHost.vue';
import { preloadWidgets } from '../src/widgets/registry';
import { readService } from '../src/widgets/serviceStats';
import { WIDGETS } from '../src/model/widgets';
import { feedFor } from '../src/model/feeds';
import { defaultBoard } from '../src/model/defaultLayout';
import { setDisplayTimeZone } from '../src/lib/format';
import type { GraphDoc, InboxDoc, MetricsDoc, Resource, Series } from '../src/model/types';
import { harness } from './harness';

const NOW = '2026-10-03T14:05:00Z';
const now = Date.parse(NOW);

beforeAll(() => preloadWidgets());
beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'] });
  vi.setSystemTime(new Date('2026-10-03T14:05:10Z'));
  setDisplayTimeZone('UTC');
});
afterEach(() => {
  vi.useRealTimers();
  document.body.innerHTML = '';
});

const s = (value: number | null, unit = 'count'): Series => ({ value, unit, as_of: NOW, history: value === null ? [] : Array(48).fill(value) });

type Data = { graph: GraphDoc; metrics: MetricsDoc; inbox: InboxDoc };

function home(only?: string[]): Data {
  const resources: Record<string, Resource> = {};
  const series: Record<string, Series> = {};
  const app = (inst: string, type: string, name: string, metrics: Record<string, Series>, attrs: Record<string, unknown> = {}, more: Partial<Resource> = {}) => {
    if (only && !only.includes(type)) return;
    const id = `${type}:${inst}/app`;
    resources[id] = { id, kind: 'app', name, state: 'running', health: 'ok', attrs: { type, ...attrs }, ...more };
    for (const [k, v] of Object.entries(metrics)) series[`${id}#${k}`] = v;
  };
  app('i_li', 'lidarr', 'Lidarr', { queue: s(1), wanted: s(16), 'missing.albums': s(14) }, {
    calendar: [{ at: '2026-10-06T00:00:00Z', title: 'Marlow Avenue', detail: 'Night Ferries · EP' }],
  });
  app('i_tr', 'transmission', 'Transmission', { 'dl.mbps': s(18.8, 'Mb/s'), 'ul.mbps': s(3.8, 'Mb/s'), leeching: s(2), seeding: s(6), queued: s(2) });
  app('i_nz', 'nzbget', 'NZBGet', { 'dl.mbps': s(0, 'Mb/s'), queue: s(3), 'left.gib': s(6.7, 'GiB') }, { paused: true }, { state: 'paused' });
  app('i_nc', 'nextcloud', 'Nextcloud', { users: s(6), 'users.active': s(1), 'users.24h': s(4), files: s(248113), shares: s(41), 'free.gib': s(912, 'GiB') });
  app('i_ab', 'audiobookshelf', 'Audiobookshelf', { streams: s(1), books: s(124), 'books.hours': s(457.8, 'h'), podcasts: s(9), 'podcasts.hours': s(600.4, 'h') });
  app('i_pn', 'paperlessngx', 'Paperless-ngx', { documents: s(2731), inbox: s(7) });
  app('i_st', 'speedtesttracker', 'Speedtest Tracker', { 'dl.mbps': s(941.2, 'Mb/s'), 'ul.mbps': s(41.2, 'Mb/s'), 'ping.ms': s(12.3, 'ms') });
  app('i_se', 'seerr', 'Overseerr', { 'requests.pending': s(3), 'requests.processing': s(2), 'requests.available': s(389) });
  return {
    graph: { resources, relations: {} },
    metrics: { series, history: { start: '2026-10-02T14:35:00Z', end: NOW, step_s: 1800, points: 48 } },
    inbox: { now: NOW, incidents: {} },
  };
}

function read(id: string, data = home()) {
  const r = data.graph.resources[id]!;
  return readService(data.graph, data.metrics, r, now);
}

describe('the service tile reads the breadth integrations', () => {
  it('takes each type’s primary stat and facts', () => {
    const cases: [string, string, string, string[]][] = [
      ['lidarr:i_li/app', '1', 'queued', ['16 wanted']],
      ['transmission:i_tr/app', '18.8', 'Mb/s ↓', ['2 leeching', '6 seeding']],
      ['nzbget:i_nz/app', '0', 'Mb/s ↓', ['3 in the queue', '6.7 GiB left']],
      ['nextcloud:i_nc/app', '1', 'active', ['6 users', '248,113 files', '912 GiB free']],
      ['audiobookshelf:i_ab/app', '1', 'listening', ['124 books', '9 podcasts']],
      ['paperlessngx:i_pn/app', '2,731', 'documents', ['7 in the inbox']],
      ['speedtesttracker:i_st/app', '941', 'Mb/s ↓', ['41.2 Mb/s ↑', '12.3 ms ping']],
      ['seerr:i_se/app', '3', 'pending', ['2 processing', '389 available']],
    ];
    for (const [id, text, unit, facts] of cases) {
      const r = read(id);
      expect([id, r.text, r.unit, r.facts.map((f) => `${f.value} ${f.label}`)]).toEqual([id, text, unit, facts]);
    }
    expect(read('lidarr:i_li/app').context).toBe('next: Marlow Avenue 00:00');
  });

  it('says when there is nothing to show, never 0 as a problem', () => {
    const data = home();
    data.metrics.series['audiobookshelf:i_ab/app#streams'] = s(0);
    data.metrics.series['transmission:i_tr/app#leeching'] = s(0);
    data.metrics.series['transmission:i_tr/app#dl.mbps'] = s(0, 'Mb/s');
    expect(read('audiobookshelf:i_ab/app', data).empty).toBe('Nobody listening');
    expect(read('transmission:i_tr/app', data).empty).toBe('Idle');
    data.metrics.series['audiobookshelf:i_ab/app#streams'] = s(null);
    expect(read('audiobookshelf:i_ab/app', data).text).toBe('—');
  });

  it('names the integration beside an instance’s own name', () => {
    const h = harness(home());
    const w = h.mountIn(WidgetHost, { item: { id: 'service@seerr:i_se/app', type: 'service' }, size: 'small' });
    expect(w.find('.tn').text()).toBe('Overseerr');
    expect(w.find('.tc').text()).toBe('Seerr');
    w.unmount();
  });
});

describe('the media and download tiles read them', () => {
  it('offers Lidarr to Queue and Up next, Transmission and NZBGet to Downloads', () => {
    const g = home(['lidarr', 'transmission', 'nzbget']).graph;
    expect(feedFor(WIDGETS['arr']!, g, now).candidates.map((r) => r.id)).toEqual(['lidarr:i_li/app']);
    expect(feedFor(WIDGETS['calendar']!, g, now).available).toBe(true);
    expect(feedFor(WIDGETS['downloads']!, home(['nzbget']).graph, now).available).toBe(true);
    expect(WIDGETS['downloads']!.accepts?.types).toEqual(['qbittorrent', 'transmission', 'sabnzbd', 'nzbget']);
    const ids = defaultBoard(g, now).sections.flatMap((sec) => sec.items.map((it) => it.id));
    expect(ids).toEqual(expect.arrayContaining(['arr@lidarr:i_li/app', 'downloads', 'calendar']));
  });

  it('shows Transmission like qBittorrent, with NZBGet as a quiet row', () => {
    const h = harness(home(['transmission', 'nzbget']));
    const w = h.mountIn(WidgetHost, { item: { id: 'downloads', type: 'downloads' }, size: 'wide' });
    const text = w.text();
    expect(text).toContain('Transmission');
    expect(text).toContain('2 leeching');
    expect(text).toContain('NZBGetpaused (by you)');
    expect(text).not.toMatch(/undefined|NaN|null\b/);
    w.unmount();
  });

  it('shows a paused NZBGet on its own as paused by you', () => {
    const h = harness(home(['nzbget']));
    const w = h.mountIn(WidgetHost, { item: { id: 'downloads@nzbget:i_nz/app', type: 'downloads' }, size: 'small' });
    expect(w.text()).toContain('NZBGet');
    expect(w.find('article.tile').classes()).not.toContain('is-attn');
    w.unmount();
  });

  it('lists Lidarr’s albums in Up next', () => {
    const h = harness(home(['lidarr']));
    const w = h.mountIn(WidgetHost, { item: { id: 'calendar', type: 'calendar' }, size: 'wide' });
    expect(w.text()).toContain('Marlow Avenue');
    w.unmount();
  });
});
