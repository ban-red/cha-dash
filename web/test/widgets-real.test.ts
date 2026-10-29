/**
 * The widget catalog on real-instance-shaped graphs: resources are
 * `<integration>:<instance id>/…` (07 · Wave 5 conventions) with the keys
 * each integration's README says it publishes, so nothing here relies on the
 * Demo Home's fixed IDs.
 */
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import type { SizeId } from '@chadash/layout';
import WidgetHost from '../src/widgets/WidgetHost.vue';
import { preloadWidgets } from '../src/widgets/registry';
import { WIDGETS } from '../src/model/widgets';
import { feedFor } from '../src/model/feeds';
import { setDisplayTimeZone } from '../src/lib/format';
import type { GraphDoc, InboxDoc, MetricsDoc, Resource, Series } from '../src/model/types';
import type { TilesDoc } from '../src/stores/tiles';
import { harness } from './harness';

const NOW = '2026-10-03T14:05:00Z';

beforeAll(() => preloadWidgets());

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'] });
  vi.setSystemTime(new Date('2026-10-03T14:05:10Z'));
  setDisplayTimeZone('UTC');
});
afterEach(() => {
  vi.useRealTimers();
  document.body.innerHTML = '';
  history.replaceState(null, '', '/');
});

type Data = { graph: GraphDoc; metrics: MetricsDoc; inbox: InboxDoc };

const hist = (from: number, to: number, n = 48): number[] => Array.from({ length: n }, (_, i) => Math.round((from + ((to - from) * i) / (n - 1)) * 10) / 10);
const s = (value: number | null, more: Partial<Series> = {}): Series => ({ value, as_of: NOW, history: value === null ? [] : hist(value * 0.8, value), ...more });

function home(): Data {
  const R: Resource[] = [];
  const rel: [string, string, string][] = [];
  const M: Record<string, Series> = {};
  const add = (r: Resource, monitoredBy?: string) => {
    R.push(r);
    if (monitoredBy) rel.push([`integration:${monitoredBy}`, 'monitors', r.id]);
  };
  const integ = (id: string, type: string, health: Resource['health'] = 'ok') => add({ id: `integration:${id}`, kind: 'integration', name: id, health, attrs: { type } });
  const appR = (inst: string, type: string, name: string, attrs: Record<string, unknown> = {}, more: Partial<Resource> = {}) => {
    integ(inst, type);
    add({ id: `${type}:${inst}/app`, kind: 'app', name, state: 'running', health: 'ok', attrs: { type, ...attrs }, links: [{ url: `https://${type}.home.example` }], ...more }, inst);
  };

  // Proxmox VE: a node with guests and local storage, a second node with none.
  integ('i_p', 'proxmox');
  add({ id: 'proxmox:i_p/node/wren', kind: 'host', name: 'wren', state: 'online', health: 'ok', labels: { 'chadash.role': 'network-core' }, attrs: { platform: 'pve', node: 'wren', mem_gb: 16, cores: 4, boot_at: '2026-09-14T18:54:25Z' } }, 'i_p');
  add({ id: 'proxmox:i_p/node/egret', kind: 'host', name: 'egret', state: 'online', health: 'ok', attrs: { platform: 'pve', node: 'egret', mem_gb: 32 } }, 'i_p');
  add({ id: 'proxmox:i_p/qemu/100', kind: 'vm', name: 'opnsense', state: 'running', desired: 'running', health: 'ok', attrs: { vmid: 100, onboot: true, started_at: '2026-09-14T18:56:25Z', 'backup.last_ok': '2026-10-03T02:03:00Z', 'backup.last_status': 'ok' } });
  add({ id: 'proxmox:i_p/lxc/101', kind: 'lxc', name: 'tailscale', state: 'running', desired: 'running', health: 'ok', attrs: { vmid: 101, onboot: true, started_at: '2026-09-14T18:57:25Z', lock: 'backup' } });
  add({ id: 'proxmox:i_p/qemu/200', kind: 'vm', name: 'sandbox', state: 'stopped', health: 'ok', attrs: { vmid: 200, onboot: false } });
  add({ id: 'proxmox:i_p/node/wren/storage/local-zfs', kind: 'storage', name: 'local-zfs', state: 'online', health: 'ok', attrs: { size_tb: 0.5, type: 'zfspool' } });
  rel.push(['proxmox:i_p/qemu/100', 'runs_on', 'proxmox:i_p/node/wren'], ['proxmox:i_p/lxc/101', 'runs_on', 'proxmox:i_p/node/wren'], ['proxmox:i_p/qemu/200', 'runs_on', 'proxmox:i_p/node/wren']);
  rel.push(['proxmox:i_p/node/wren/storage/local-zfs', 'part_of', 'proxmox:i_p/node/wren']);
  M['proxmox:i_p/node/wren#cpu.pct'] = s(9, { unit: '%', band: [0, 20] });
  M['proxmox:i_p/node/wren#mem.pct'] = s(81, { unit: '%', band: [55, 85], warn: 90, crit: 98 });
  M['proxmox:i_p/node/egret#cpu.pct'] = s(4, { unit: '%' });
  M['proxmox:i_p/node/egret#mem.pct'] = s(30, { unit: '%', warn: 90, crit: 98 });
  M['proxmox:i_p/qemu/100#cpu.pct'] = s(12, { unit: '%' });
  M['proxmox:i_p/qemu/100#mem.pct'] = s(60, { unit: '%' });
  M['proxmox:i_p/node/wren/storage/local-zfs#used.pct'] = s(31, { unit: '%', warn: 85, crit: 95 });

  // Unraid: the host, its array with a spun-down disk, a pool, a container on the host itself.
  integ('i_u', 'unraid');
  add({ id: 'unraid:i_u/host', kind: 'host', name: 'magpie', state: 'online', health: 'ok', attrs: { platform: 'unraid', version: '7.3', mem_gb: 64 } }, 'i_u');
  add({ id: 'unraid:i_u/array', kind: 'array', name: 'array', state: 'online', health: 'ok', attrs: { size_tb: 62, parity: 'valid', parity_checked_at: '2026-09-26T17:00:00Z' } });
  add({ id: 'unraid:i_u/disk/parity', kind: 'disk', name: 'parity', state: 'online', health: 'ok', attrs: { role: 'parity', size_tb: 16 } });
  add({ id: 'unraid:i_u/disk/disk1', kind: 'disk', name: 'disk1', state: 'idle', health: 'ok', attrs: { role: 'data', size_tb: 14 } });
  add({ id: 'unraid:i_u/pool/cache', kind: 'pool', name: 'cache', state: 'online', health: 'ok', attrs: { size_tb: 2 } });
  add({ id: 'unraid:i_u/container/plex', kind: 'container', name: 'plex', state: 'running', health: 'ok', attrs: { image: 'plexinc/pms-docker:1.41', started_at: '2026-09-16T16:05:15Z' } });
  rel.push(['unraid:i_u/array', 'part_of', 'unraid:i_u/host'], ['unraid:i_u/pool/cache', 'part_of', 'unraid:i_u/host']);
  rel.push(['unraid:i_u/disk/parity', 'part_of', 'unraid:i_u/array'], ['unraid:i_u/disk/disk1', 'part_of', 'unraid:i_u/array']);
  rel.push(['unraid:i_u/container/plex', 'runs_on', 'unraid:i_u/host']);
  M['unraid:i_u/host#cpu.pct'] = s(14, { unit: '%' });
  M['unraid:i_u/host#mem.pct'] = s(42, { unit: '%', warn: 90, crit: 98 });
  M['unraid:i_u/array#used.pct'] = s(71, { unit: '%', warn: 85, crit: 95 });
  M['unraid:i_u/disk/parity#temp.c'] = s(38, { unit: '°C', warn: 50, crit: 55 });
  M['unraid:i_u/disk/disk1#used.pct'] = s(89, { unit: '%', warn: 90, crit: 97 });
  M['unraid:i_u/disk/disk1#temp.c'] = { value: null, unit: '°C', warn: 50, crit: 55, history: [] };
  M['unraid:i_u/pool/cache#used.pct'] = s(44, { unit: '%' });

  // Docker: an engine with a running container and one OOM-killed.
  integ('i_d', 'docker');
  add({ id: 'docker:i_d/engine', kind: 'engine', name: 'rook', state: 'running', health: 'ok', attrs: { endpoint: 'tcp://proxy.home.example:2375' } }, 'i_d');
  add({ id: 'docker:i_d/container/sonarr', kind: 'container', name: 'sonarr', state: 'running', desired: 'running', health: 'ok', attrs: { image: 'lscr.io/linuxserver/sonarr:4.0.9', started_at: '2026-10-01T15:00:34Z' } });
  add({ id: 'docker:i_d/container/llama', kind: 'container', name: 'llama-swap', state: 'exited', desired: 'running', health: { status: 'down', reason: 'OOM-killed', since: '2026-10-03T03:12:00Z' }, attrs: { image: 'ghcr.io/x/llama-swap:latest', exit_code: 137, oom_killed: true, finished_at: '2026-10-03T03:12:00Z' } });
  rel.push(['docker:i_d/container/sonarr', 'runs_on', 'docker:i_d/engine'], ['docker:i_d/container/llama', 'runs_on', 'docker:i_d/engine']);

  // OPNsense: router and WAN, without Lobby: Dashboard (no CPU).
  integ('i_o', 'opnsense');
  add({ id: 'opnsense:i_o/router', kind: 'router', name: 'OPNsense', state: 'running', health: 'ok', attrs: { platform: 'opnsense', interfaces: 4 } }, 'i_o');
  add({ id: 'opnsense:i_o/wan', kind: 'wan', name: 'WAN', state: 'online', health: 'ok', attrs: { interface: 'wan' } });
  rel.push(['opnsense:i_o/wan', 'part_of', 'opnsense:i_o/router']);
  M['opnsense:i_o/wan#rx.mbps'] = s(9.3, { unit: 'Mb/s' });
  M['opnsense:i_o/wan#tx.mbps'] = s(5.3, { unit: 'Mb/s' });
  M['opnsense:i_o/wan#rx.gb_30d'] = s(852, { unit: 'GB' });
  M['opnsense:i_o/wan#tx.gb_30d'] = s(418, { unit: 'GB' });

  // Frigate with two cameras.
  appR('i_f', 'frigate', 'Frigate', { cameras: 2, version: '0.18.0' });
  for (const [cam, name] of [['driveway', 'Driveway'], ['garage', 'Garage']] as const) {
    add({ id: `frigate:i_f/camera/${cam}`, kind: 'camera', name, state: 'online', health: 'ok', attrs: { camera: cam, record: true, 'last_event.label': 'person', 'last_event.at': '2026-10-03T13:24:02Z', 'last_event.score': 0.79 } }, 'i_f');
    rel.push(['frigate:i_f/app', 'exposes', `frigate:i_f/camera/${cam}`]);
    M[`frigate:i_f/camera/${cam}#detections.24h`] = s(5, { unit: 'count' });
  }

  // PBS: a datastore that expects to be full in three weeks.
  appR('i_b', 'pbs', 'PBS');
  add({ id: 'pbs:i_b/datastore/main', kind: 'backup_target', name: 'main', state: 'online', health: 'ok', attrs: { capacity_tb: 2, full_in_days: 21, prune_job: 'enabled', retention: 'keep-daily 7' } });
  rel.push(['pbs:i_b/datastore/main', 'part_of', 'pbs:i_b/app']);
  M['pbs:i_b/datastore/main#used.pct'] = s(84, { unit: '%', warn: 90, crit: 96 });
  M['pbs:i_b/app#tasks.failed_24h'] = s(0, { unit: 'count' });

  // Built-in checks and an Uptime Kuma monitor, all kind check.
  integ('i_c', 'checks');
  add({ id: 'check:i_c/sonarr', kind: 'check', name: 'Sonarr', state: 'online', health: 'ok', attrs: { check: 'http', target: 'https://sonarr.home.example', status_code: 200 } }, 'i_c');
  add({ id: 'check:i_c/nas', kind: 'check', name: 'NAS', state: 'error', health: { status: 'down', since: '2026-10-03T14:02:00Z', reason: 'connection refused' }, attrs: { check: 'tcp', target: '10.20.30.50:445' } }, 'i_c');
  M['check:i_c/sonarr#http.ms'] = s(54, { unit: 'ms', band: [1, 150], warn: 800, crit: 2000 });
  M['check:i_c/nas#tcp.ms'] = { value: null, unit: 'ms', as_of: NOW, warn: 200, history: [...hist(3, 4, 44), null, null, null, null] };
  appR('i_k', 'uptimekuma', 'Uptime Kuma', { monitors: 1, status_page: 'home' });
  add({ id: 'uptimekuma:i_k/monitor/1', kind: 'check', name: 'Immich', state: 'online', health: 'ok', attrs: { check: 'http', monitor_id: 1, uptime_24h: 99.5, target: 'https://photos.home.example' } }, 'i_k');
  rel.push(['uptimekuma:i_k/monitor/1', 'part_of', 'uptimekuma:i_k/app']);
  M['uptimekuma:i_k/app#monitors.up'] = s(1, { unit: 'count' });
  M['uptimekuma:i_k/app#monitors.down'] = s(0, { unit: 'count', history: Array(48).fill(0) });
  M['uptimekuma:i_k/monitor/1#http.ms'] = s(120, { unit: 'ms', warn: 800 });

  // Weather, as integrations/weather/README.md's example.
  integ('i_w', 'weather');
  add(
    {
      id: 'weather:i_w/here',
      kind: 'weather',
      name: 'Weather',
      state: 'online',
      health: 'ok',
      attrs: {
        type: 'weather', provider: 'open-meteo', location: 'Greenwich', units: 'metric', observed_at: '2026-10-03T15:00:00+01:00', timezone: 'Europe/London', utc_offset_s: 3600,
        code: 3, condition: 'overcast', summary: 'Overcast', is_day: true, high_c: 15.1, low_c: 9.8, precip_pct: 60,
        hourly: [
          { at: '2026-10-03T16:00:00+01:00', temp_c: 14, code: 3, condition: 'overcast', precip_pct: 15, is_day: true },
          { at: '2026-10-03T17:00:00+01:00', temp_c: 13.6, code: 61, condition: 'rain', precip_pct: 45, is_day: true },
          { at: '2026-10-03T18:00:00+01:00', temp_c: null, code: null, condition: null, precip_pct: null, is_day: null },
        ],
        daily: [{ date: '2026-10-03', code: 61, condition: 'rain', summary: 'Light rain', high_c: 15.1, low_c: 9.8, precip_mm: 2.4, precip_pct: 60 }],
        attribution: 'Weather data by Open-Meteo.com', attribution_url: 'https://open-meteo.com/',
      },
    },
    'i_w',
  );
  M['weather:i_w/here#temp.c'] = s(14.2, { unit: '°C', band: [9.8, 15.1] });
  M['weather:i_w/here#feels.c'] = s(12.9, { unit: '°C' });
  M['weather:i_w/here#wind.kmh'] = s(18, { unit: 'km/h' });

  // Media, downloads and DNS: Tier 0 and the specs.
  appR('i_j', 'jellyfin', 'Jellyfin', { movies: 812, shows: 120, episodes: 4300 });
  M['jellyfin:i_j/app#streams'] = s(1, { unit: 'count' });
  M['jellyfin:i_j/app#streams.transcode'] = s(1, { unit: 'count' });
  appR('i_t', 'tautulli', 'Tautulli');
  M['tautulli:i_t/app#streams'] = s(2, { unit: 'count' });
  M['tautulli:i_t/app#streams.transcode'] = s(1, { unit: 'count' });
  M['tautulli:i_t/app#bandwidth.mbps'] = s(12.3, { unit: 'Mb/s' });
  appR('i_s', 'sonarr', 'Sonarr', {
    calendar: [
      { at: '2026-10-04T04:00:00Z', title: 'Starling Point', detail: 'S05E01' },
      { at: '2026-10-05T02:00:00Z', title: 'Quiet Valley', detail: 'S01E01', unmonitored: true },
      { at: '2026-10-05T20:00:00Z', title: 'Severance', detail: 'S02E04' },
    ],
  });
  M['sonarr:i_s/app#queue'] = s(3, { unit: 'count' });
  M['sonarr:i_s/app#wanted'] = s(12, { unit: 'count' });
  appR('i_r', 'radarr', 'Radarr 4K', { calendar: [{ at: '2026-10-06T00:00:00Z', title: 'Dune: Part Two', detail: 'digital release' }] }, { caps: ['media.calendar'] });
  M['radarr:i_r/app#queue'] = s(0, { unit: 'count' });
  appR('i_bz', 'bazarr', 'Bazarr');
  M['bazarr:i_bz/app#wanted'] = s(40, { unit: 'count', history: hist(80, 40) });
  M['bazarr:i_bz/app#missing.episodes'] = s(34, { unit: 'count' });
  M['bazarr:i_bz/app#missing.movies'] = s(6, { unit: 'count' });
  appR('i_m', 'mealie', 'Mealie', { recipes: 87, categories: 8, tags: 240 });
  M['mealie:i_m/app#recipes'] = s(87, { unit: 'count' });
  appR('i_l', 'linkding', 'Linkding', { archived: 20, tags: 80 });
  M['linkding:i_l/app#bookmarks'] = s(1500, { unit: 'count' });
  appR('i_kk', 'karakeep', 'Karakeep', { favorites: 12 });
  M['karakeep:i_kk/app#bookmarks'] = s(1202, { unit: 'count', stale: true, as_of: '2026-10-03T09:04:30Z' });
  appR('i_q', 'qbittorrent', 'qBittorrent', { connection_status: 'connected' });
  M['qbittorrent:i_q/app#dl.mbps'] = s(24.5, { unit: 'Mb/s' });
  M['qbittorrent:i_q/app#leeching'] = s(3, { unit: 'count' });
  M['qbittorrent:i_q/app#seeding'] = s(70, { unit: 'count' });
  appR('i_z', 'sabnzbd', 'SABnzbd', { paused: true }, { state: 'paused' });
  M['sabnzbd:i_z/app#dl.mbps'] = s(0, { unit: 'Mb/s' });
  M['sabnzbd:i_z/app#queue'] = s(3, { unit: 'count' });
  M['sabnzbd:i_z/app#left.gib'] = s(12.4, { unit: 'GiB' });
  appR('i_h', 'pihole', 'Pi-hole', { blocking: 'enabled' });
  M['pihole:i_h/app#blocked.pct'] = s(18.2, { unit: '%' });
  M['pihole:i_h/app#blocklist'] = s(123456, { unit: 'count' });
  M['pihole:i_h/app#queries.24h'] = s(41000, { unit: 'count' });
  M['pihole:i_h/app#clients'] = s(14, { unit: 'count' });
  appR('i_g', 'adguard', 'AdGuard Home', {}, { health: { status: 'attention', reason: 'Protection is paused for another 5 min' } });
  M['adguard:i_g/app#blocked.pct'] = s(9.7, { unit: '%' });
  M['adguard:i_g/app#blocklist'] = s(80000, { unit: 'count' });
  appR('i_ha', 'homeassistant', 'Home', { people_total: 2, lights_total: 20, switches_total: 40, entities: 512 });
  M['homeassistant:i_ha/app#people_home'] = s(1, { unit: 'count' });
  M['homeassistant:i_ha/app#lights_on'] = s(3, { unit: 'count' });
  M['homeassistant:i_ha/app#power.kw'] = s(0.9, { unit: 'kW', band: [0.6, 2.2] });

  const resources: Record<string, Resource> = {};
  for (const r of R) resources[r.id] = r;
  // Karakeep's integration can't read it: a repair.
  resources['integration:i_kk']!.health = 'repair';
  resources['integration:i_kk']!.attrs = { type: 'karakeep', 'health.reason': 'stats endpoint returned 404' };
  const relations: GraphDoc['relations'] = {};
  for (const [from, type, to] of rel) relations[`${from} ${type} ${to}`] = { from, type, to };
  const metrics: MetricsDoc = { series: M, history: { start: '2026-10-02T14:35:00Z', end: NOW, step_s: 1800, points: 48 } };
  const inbox: InboxDoc = {
    now: NOW,
    incidents: {
      'i_kk.repair': { id: 'i_kk.repair', class: 'repair', severity: 'attention', status: 'open', title: "Karakeep's API changed", summary: '', target: 'integration:i_kk', opened_at: '2026-10-03T09:05:00Z' },
    },
  };
  return { graph: { resources, relations }, metrics, inbox };
}

/** A real target per widget type. */
const TARGETS: Record<string, string | undefined> = {
  host: 'proxmox:i_p/node/wren',
  arr: 'sonarr:i_s/app',
  service: 'pihole:i_h/app',
  metric: 'proxmox:i_p/node/wren#mem.pct',
  storage: 'pbs:i_b/datastore/main',
  guests: 'proxmox:i_p/node/wren',
  containers: 'docker:i_d/engine',
  unraid: 'unraid:i_u/array',
  backups: 'pbs:i_b/datastore/main',
  opnsense: 'opnsense:i_o/router',
  cameras: 'frigate:i_f/app',
  homeassistant: 'homeassistant:i_ha/app',
  plex: 'jellyfin:i_j/app',
  downloads: 'qbittorrent:i_q/app',
  check: undefined,
  weather: 'weather:i_w/here',
  traefik: undefined,
  uptimekuma: 'uptimekuma:i_k/app',
};

function mountReal(type: string, size: SizeId, target = TARGETS[type], data: Data = home(), tiles?: TilesDoc) {
  const h = harness(data);
  if (tiles) h.board.tiles.value = tiles;
  const w = h.mountIn(WidgetHost, { item: { id: target ? `${type}@${target}` : type, type }, size });
  return { ...h, w, tile: () => w.find('article.tile') };
}

describe('every widget on real instances', () => {
  for (const [type, spec] of Object.entries(WIDGETS)) {
    it(`${type}: every declared size, without the Demo Home’s IDs`, () => {
      for (const size of spec.sizes) {
        const { w, tile } = mountReal(type, size);
        expect(tile().classes(), `${type} ${size}`).toContain(`s-${size}`);
        expect(w.text(), `${type} ${size}: ${w.text()}`).not.toMatch(/undefined|NaN|\[object|null\b/);
        w.unmount();
      }
    });
  }
});

describe('service tile on real instances', () => {
  const stat = (target: string, size: SizeId = 'small') => {
    const m = mountReal('service', size, target);
    return { ...m, v: m.w.find('.v').text() };
  };

  it('reads each type’s primary stat and context line', () => {
    expect(stat('pihole:i_h/app').v).toBe('18.2% blocked');
    expect(stat('pihole:i_h/app').w.find('.svc-ctx').text()).toBe('123k domains on the blocklist');
    expect(stat('jellyfin:i_j/app').v).toBe('1playing');
    expect(stat('tautulli:i_t/app').v).toBe('2playing');
    expect(stat('bazarr:i_bz/app').v).toBe('40subtitles missing');
    expect(stat('bazarr:i_bz/app').w.find('.svc-ctx').text()).toBe('down 40 in 24 h');
    expect(stat('mealie:i_m/app').v).toBe('87recipes');
    expect(stat('linkding:i_l/app').v).toBe('1,500bookmarks');
    expect(stat('sonarr:i_s/app').v).toBe('3queued');
    expect(stat('sonarr:i_s/app').w.find('.svc-ctx').text()).toBe('next: Starling Point 04:00');
    expect(stat('radarr:i_r/app').v).toBe('Queue clear');
    expect(stat('uptimekuma:i_k/app').v).toBe('1/1 up');
    expect(stat('qbittorrent:i_q/app').v).toBe('24.5Mb/s ↓');
  });

  it('shows the integration type beside an instance’s own name', () => {
    const { w } = mountReal('service', 'small', 'radarr:i_r/app');
    expect(w.find('.tn').text()).toBe('Radarr 4K');
    expect(w.find('.tc').text()).toBe('Radarr');
  });

  it('shows a protection pause as attention, the reason from the hub', () => {
    const { w, tile } = mountReal('service', 'small', 'adguard:i_g/app');
    expect(w.find('.v').text()).toBe('9.7% blocked');
    expect(tile().classes()).toContain('is-attn');
    expect(w.find('.note').text()).toContain('Protection is paused for another 5 min');
  });

  it('shows a paused SABnzbd as paused by you, not a problem', () => {
    const { w, tile } = mountReal('service', 'small', 'sabnzbd:i_z/app');
    expect(tile().classes()).toContain('is-off');
    expect(tile().classes()).not.toContain('is-attn');
    expect(w.find('.note').text()).toBe('Paused (by you)');
  });

  it('keeps Karakeep’s last-known count while its integration needs repair', () => {
    const { w, tile } = mountReal('service', 'small', 'karakeep:i_kk/app');
    expect(tile().classes()).toContain('is-stale');
    expect(w.find('.v').text()).toBe('1,202bookmarks');
    expect(w.find('.note').text()).toContain("Karakeep's API changed.");
  });

  it('shows a plain URL as its status code, latency and uptime', () => {
    const { w } = mountReal('service', 'wide', 'check:i_c/sonarr');
    expect(w.find('.v').text()).toBe('200HTTP');
    expect(w.find('.sub').text()).toBe('54 ms · 100% up · 24 h');
    const g = mountReal('service', 'glyph', 'check:i_c/sonarr');
    expect(g.w.find('.gv .g-link').exists()).toBe(true);
  });

  it('takes its app from the tile config’s instance when the target is a hand-made tile', () => {
    const { w } = mountReal('service', 'small', 'tile:abc', home(), { 'service@tile:abc': { name: 'Recipes', bind: { instance: 'i_m' } } });
    expect(w.find('.tn').text()).toBe('Recipes');
    expect(w.find('.v').text()).toBe('87recipes');
  });
});

describe('a tile bound to an instance that has never synced', () => {
  /** The import's item ids: dedicated widgets without a target, `arr` with the app it expects. */
  const ITEMS: Record<string, string> = {
    plex: 'plex',
    homeassistant: 'homeassistant',
    cameras: 'cameras',
    opnsense: 'opnsense',
    traefik: 'traefik',
    uptimekuma: 'uptimekuma',
    downloads: 'downloads',
    arr: 'arr@sonarr:i_new/app',
    service: 'service@homepage:media/new',
  };
  /** The home's healthy apps are all there to fall back to (Tautulli, Frigate, a router); the bound instance has only its own resource, needing repair. */
  function broken(type: string): Data {
    const d = home();
    const g = d.graph;
    g.resources['integration:i_new'] = { id: 'integration:i_new', kind: 'integration', name: 'New', health: { status: 'repair', reason: 'the token was rejected' }, attrs: { type } };
    d.inbox.incidents['i_new.repair'] = { id: 'i_new.repair', class: 'repair', severity: 'attention', status: 'open', title: 'It rejected its credential', summary: '', target: 'integration:i_new', opened_at: NOW };
    return d;
  }

  for (const [type, id] of Object.entries(ITEMS)) {
    it(`${type}: says it needs repair, not another app's state`, () => {
      const h = harness(broken(type));
      h.board.tiles.value = { [id]: { bind: { instance: 'i_new' } } };
      for (const size of WIDGETS[type]!.sizes) {
        const w = h.mountIn(WidgetHost, { item: { id, type }, size });
        const tile = w.find('article.tile');
        expect(tile.attributes('aria-label'), `${type} ${size}`).toMatch(/needs repair/);
        // The service tile says why in its own words, without a button.
        if (type !== 'service' && size !== 'glyph' && !size.startsWith('strip')) expect(w.find('.note button').text(), `${type} ${size}`).toBe('Reconnect');
        expect(w.text(), `${type} ${size}`).not.toMatch(/\bidle\b|WAN up|recording|undefined|NaN/);
        w.unmount();
      }
    });
  }

  it('shows the bound instance’s app once it has one, over the first of its type', () => {
    const d = broken('plex');
    d.graph.resources['plex:i_new/app'] = { id: 'plex:i_new/app', kind: 'app', name: 'Plex', state: 'running', health: 'ok', attrs: { type: 'plex', movies: 12 } };
    d.graph.relations['integration:i_new monitors plex:i_new/app'] = { from: 'integration:i_new', type: 'monitors', to: 'plex:i_new/app' };
    const h = harness(d);
    h.board.tiles.value = { plex: { bind: { instance: 'i_new' } } };
    const w = h.mountIn(WidgetHost, { item: { id: 'plex', type: 'plex' }, size: 'wide' });
    expect(w.text()).toContain('12 movies');
    expect(w.find('article.tile').attributes('aria-label')).toMatch(/needs repair/);
  });
});

describe('infrastructure widgets on real instances', () => {
  it('reads a Proxmox node without temperatures, with its guests and storage', () => {
    const { w } = mountReal('host', 'large');
    expect(w.find('.v').text()).toBe('81% memory');
    expect(w.text()).not.toContain('°');
    expect(w.find('.sub').text()).toContain('2/3 guests');
    expect(w.find('.host-disks').text()).toContain('local-zfs');
    expect(w.findAll('.list .li')[0]!.find('.g-prog').exists()).toBe(true); // tailscale backing up
  });

  it('counts an Unraid host’s own containers', () => {
    const { w } = mountReal('host', 'small', 'unraid:i_u/host');
    expect(w.find('.tf').text()).toContain('1/1 containers');
  });

  it('lists a node’s guests, the stopped one dimmed', () => {
    const { w } = mountReal('guests', 'tall');
    const rows = w.findAll('.li');
    expect(rows.map((r) => r.find('.t').text())).toEqual(['tailscale', 'opnsense', 'sandbox']);
    expect(rows[2]!.classes()).toContain('dim');
  });

  it('lists an engine’s containers, the OOM-killed one first and named so', () => {
    const { w, tile } = mountReal('containers', 'tall');
    expect(tile().classes()).toContain('is-down');
    expect(w.find('.li').text()).toContain('llama-swap');
    expect(w.find('.li').text()).toContain('exited (137) at 03:12 · OOM-killed');
    const unraid = mountReal('containers', 'large', 'unraid:i_u/host');
    expect(unraid.w.find('.sr:not(.head)').text()).toContain('1.41');
  });

  it('lists a container two sources report once, on its host', () => {
    const d = home();
    const add = (r: Resource) => (d.graph.resources[r.id] = r);
    add({ id: 'docker:i_d2/engine', kind: 'engine', name: 'magpie', state: 'running', health: 'ok' });
    add({ id: 'docker:i_d2/container/plex', kind: 'container', name: 'plex', state: 'running', health: 'ok', source: 'integration:i_d2' });
    d.graph.resources['unraid:i_u/container/plex']!.source = 'integration:i_u';
    for (const [from, type, to] of [
      ['docker:i_d2/container/plex', 'runs_on', 'docker:i_d2/engine'],
      ['docker:i_d2/engine', 'runs_on', 'unraid:i_u/host'],
      ['unraid:i_u/container/plex', 'same_as', 'docker:i_d2/container/plex'],
    ] as const) {
      d.graph.relations[`${from} ${type} ${to}`] = { from, type, to };
    }
    const { w } = mountReal('containers', 'tall', 'unraid:i_u/host', d);
    expect(w.findAll('.li .t').map((t) => t.text())).toEqual(['plex']);
  });

  it('shows a spun-down disk as "—", never 0°', () => {
    const { w } = mountReal('unraid', 'tall');
    const disk1 = w.findAll('.disk').find((d) => d.text().includes('disk1'))!;
    expect(disk1.text()).toContain('—');
    expect(w.find('.v').text()).toBe('71%');
  });

  it('takes "full in" from PBS’s own estimate', () => {
    const { w } = mountReal('storage', 'small');
    expect(w.find('.lab').text()).toBe('full in ~3 weeks');
    const b = mountReal('backups', 'wide');
    expect(b.w.find('.lab').text()).toBe('full in ~3 weeks');
  });

  it('shows the router’s CPU as permission-limited without Lobby: Dashboard', () => {
    const { w, tile } = mountReal('opnsense', 'wide');
    expect(tile().attributes('aria-label')).toContain('WAN up');
    expect(w.find('.locked [aria-label="needs Lobby: Dashboard"]').exists()).toBe(true);
  });

  it('lists built-in checks and Uptime Kuma monitors together, the down one first', () => {
    const { w, tile } = mountReal('check', 'wide');
    expect(tile().classes()).toContain('is-down');
    const rows = w.findAll('.chk');
    expect(rows.map((r) => r.find('.t').text().split('\n')[0])).toEqual(['NAS10.20.30.50:445', 'Immichphotos.home.example', 'Sonarrsonarr.home.example']);
    expect(rows[0]!.text()).toContain('down since 14:02');
    const kuma = mountReal('check', 'small', 'uptimekuma:i_k/app');
    expect(kuma.w.find('.v').text()).toBe('1/1 up');
    expect(kuma.w.find('.tf').text()).toContain('99.5%');
    const one = mountReal('check', 'glyph', 'check:i_c/nas');
    expect(one.w.find('.gv [aria-label]').attributes('aria-label')).toBe('down since 14:02');
  });
});

describe('home and media widgets on real instances', () => {
  it('shows a Frigate instance’s cameras', () => {
    const { w } = mountReal('cameras', 'wide');
    expect(w.findAll('.cam')).toHaveLength(2);
    expect(w.find('.tf').text()).toContain('10 detections today');
  });

  it('reads Jellyfin as Now playing reads Plex', () => {
    const { w } = mountReal('plex', 'wide');
    expect(w.find('.v').text()).toBe('1playing');
    expect(w.find('.tc').text()).toBe('Jellyfin');
    expect(w.find('.sub').text()).toContain('1 transcoding');
    expect(w.text()).toContain('812');
  });

  it('shows a paused SABnzbd beside qBittorrent, quietly', () => {
    const { w } = mountReal('downloads', 'tall');
    const sab = w.findAll('.li').find((r) => r.text().includes('SABnzbd'))!;
    expect(sab.text()).toContain('paused (by you)');
    expect(sab.find('.g-off').exists()).toBe(true);
    const only = mountReal('downloads', 'small', 'sabnzbd:i_z/app');
    expect(only.tile().classes()).toContain('is-off');
    expect(only.w.find('.tg').text()).toContain('paused (by you)');
  });

  it('keeps a queue’s last-known count and offers Reconnect when its key is rejected', async () => {
    const d = home();
    d.graph.resources['integration:i_s']!.health = { status: 'repair', reason: 'Sonarr rejected the API key' };
    d.metrics.series['sonarr:i_s/app#queue'] = { ...d.metrics.series['sonarr:i_s/app#queue']!, stale: true, as_of: '2026-10-03T13:00:00Z' };
    const { w, tile, ui } = mountReal('arr', 'small', 'sonarr:i_s/app', d);
    expect(tile().classes()).toContain('is-stale');
    expect(w.find('.v').text()).toBe('3');
    expect(w.find('.note').text()).toContain('Sonarr rejected the API key. Values are last-known.');
    await w.find('.note button').trigger('click');
    expect(ui.state.inboxOpen).toBe(false); // no repair incident: the integration's settings instead
    expect(location.pathname).toBe('/settings/integrations/i_s');
  });

  it('reads the calendar sources the tile config names', () => {
    const { w } = mountReal('calendar', 'tall', undefined, home(), { calendar: { bind: { sources: [{ instance: 'i_r', capability: 'media.calendar' }] } } });
    expect(w.findAll('.ag-row').map((r) => r.text())).toEqual([expect.stringContaining('Dune: Part Two')]);
    const all = mountReal('calendar', 'tall');
    expect(all.w.findAll('.ag-row')).toHaveLength(3);
    expect(all.w.text()).not.toContain('Quiet Valley');
  });

  it('lists unmonitored releases only where the tile, or that source, asks', () => {
    const rows = (tiles?: TilesDoc) => mountReal('calendar', 'tall', undefined, home(), tiles).w.findAll('.ag-row').map((r) => r.text());
    const tile = rows({ calendar: { options: { unmonitored: true } } });
    expect(tile).toHaveLength(4);
    expect(tile.find((r) => r.includes('Quiet Valley'))).toContain('unmonitored');
    // The importer's per-source option: Sonarr's, so Sonarr's unmonitored row shows.
    const perSource = rows({
      calendar: { bind: { sources: [{ instance: 'i_s', capability: 'media.calendar', options: { unmonitored: true } }, { instance: 'i_r', capability: 'media.calendar' }] } },
    });
    expect(perSource.some((r) => r.includes('Quiet Valley'))).toBe(true);
    const otherSource = rows({
      calendar: { bind: { sources: [{ instance: 'i_s', capability: 'media.calendar' }, { instance: 'i_r', capability: 'media.calendar', options: { unmonitored: true } }] } },
    });
    expect(otherSource.some((r) => r.includes('Quiet Valley'))).toBe(false);
  });

  it('reads the weather source’s shape: now, high and low, the next hours, the credit', () => {
    const { w } = mountReal('weather', 'wide');
    expect(w.find('.tn').text()).toBe('Greenwich');
    expect(w.find('.v').text()).toBe('14°C');
    expect(w.find('.lab').text()).toBe('H 15° L 10°');
    expect(w.find('.sub').text()).toContain('feels 13°');
    const hours = w.findAll('.wx-hours li');
    expect(hours.map((h) => h.text())).toEqual(['1614°', '1714°', '18—°']);
    expect(hours[1]!.find('[aria-label="rain"]').exists()).toBe(true);
    expect(w.find('.wx-credit a').attributes('href')).toBe('https://open-meteo.com/');
    expect(w.find('.wx-credit').text()).toBe('Weather data by Open-Meteo.com');
  });

  it('converts for imperial, says a storm is coming, and keeps the last forecast when stale', () => {
    const d = home();
    const wx = d.graph.resources['weather:i_w/here']!;
    wx.attrs!['units'] = 'imperial';
    wx.health = { status: 'attention', reason: 'Thunderstorm expected around 18:00' };
    d.metrics.series['weather:i_w/here#temp.c'] = { ...d.metrics.series['weather:i_w/here#temp.c']!, stale: true, as_of: '2026-10-03T13:00:00Z' };
    const { w, tile } = mountReal('weather', 'small', undefined, d);
    expect(w.find('.v').text()).toBe('58°F');
    expect(tile().classes()).toEqual(expect.arrayContaining(['is-attn', 'is-stale']));
    expect(w.find('.asof').text()).toBe('as of 13:00');
    expect(w.find('.note').text()).toBe('Thunderstorm expected around 18:00');
    const glyph = mountReal('weather', 'glyph', undefined, d);
    expect(glyph.w.find('.gv').text()).toBe('58°');
  });
});

describe('what the graph can feed the new widgets', () => {
  it('feeds each new type from the kinds it reads', () => {
    const g = home().graph;
    const now = Date.parse(NOW);
    for (const t of ['guests', 'containers', 'unraid', 'check', 'bookmarks', 'plex', 'downloads', 'clock', 'weather', 'homeassistant']) {
      expect([t, feedFor(WIDGETS[t]!, g, now).available]).toEqual([t, true]);
    }
    const storage = feedFor(WIDGETS['storage']!, g, now).candidates.map((r) => r.id);
    expect(storage).toEqual(expect.arrayContaining(['pbs:i_b/datastore/main', 'unraid:i_u/array', 'unraid:i_u/pool/cache', 'proxmox:i_p/node/wren/storage/local-zfs']));
    const metric = feedFor(WIDGETS['metric']!, g, now).candidates.map((r) => r.id);
    expect(metric).toEqual(expect.arrayContaining(['proxmox:i_p/node/wren#mem.pct', 'opnsense:i_o/wan#rx.mbps', 'homeassistant:i_ha/app#power.kw']));
    const empty: GraphDoc = { resources: {}, relations: {} };
    expect(feedFor(WIDGETS['clock']!, empty, now).available).toBe(true);
    expect(feedFor(WIDGETS['weather']!, empty, now).available).toBe(false);
  });
});
