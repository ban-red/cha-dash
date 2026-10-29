/**
 * The widget catalog on the Demo Home (sim/snapshots): every widget at every
 * size it declares, in both graph shapes, plus each widget's key states
 * (docs/design/workbench.md). Real-instance graphs are in widgets-real.test.ts.
 */
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { nextTick } from 'vue';
import { flushPromises } from '@vue/test-utils';
import type { SizeId } from '@chadash/layout';
import WidgetHost from '../src/widgets/WidgetHost.vue';
import { preloadWidgets, WIDGET_COMPONENTS } from '../src/widgets/registry';
import { WIDGETS } from '../src/model/widgets';
import { absolute, setDisplayTimeZone } from '../src/lib/format';
import { applyTheme } from '../src/lib/theme';
import type { GraphDoc, Health, Incident, InboxDoc, MetricsDoc, Resource } from '../src/model/types';
import { docs, healthy, withIncidents } from './fixtures';
import { harness } from './harness';
import { engine } from './fake-engine';
import { fakeFetch } from './fake-fetch';
import { route } from '../src/lib/router';

// The on-demand widgets, fetched once: afterwards they mount without a skeleton.
beforeAll(() => preloadWidgets());

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'] });
  vi.setSystemTime(new Date('2026-10-03T14:05:10Z'));
  setDisplayTimeZone('UTC');
  applyTheme('instrument-dark');
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

type Data = { graph: GraphDoc; metrics: MetricsDoc; inbox: InboxDoc };

/** A Demo Home target for each widget type (undefined: the widget picks). */
export const DEMO_TARGETS: Record<string, string | undefined> = {
  host: 'unraid:magpie',
  arr: 'app:sonarr',
  service: 'app:sonarr',
  metric: 'pve:stork#mem.pct',
  storage: 'pve:owl/storage/local-lvm',
};

function mountWidget(data: Data | undefined, type: string, size: SizeId, target = DEMO_TARGETS[type], opts: { role?: string; demo?: boolean } = {}) {
  const h = harness(data, { demo: opts.demo === true });
  if (opts.role) h.session.state.user = { id: 'u2', username: 'kid', role: opts.role };
  const id = target ? `${type}@${target}` : type;
  const w = h.mountIn(WidgetHost, { item: { id, type }, size });
  return { ...h, w, tile: () => w.find('article.tile') };
}

const clone = <T>(x: T): T => structuredClone(x);
function setHealth(d: Data, id: string, health: Health, patch: Partial<Resource> = {}): void {
  const r = d.graph.resources[id];
  if (!r) throw new Error(`no ${id}`);
  Object.assign(r, { health, ...patch });
}
const BAD = /undefined|NaN|\[object|null\b/;

describe('the catalog', () => {
  it('has a component for every widget type, with sizes from the Workbench list', () => {
    for (const [type, spec] of Object.entries(WIDGETS)) {
      expect(WIDGET_COMPONENTS[type], type).toBeDefined();
      expect(spec.sizes, type).toContain(spec.size);
      expect(spec.sizes.every((s) => s !== 'strip-long'), type).toBe(true);
    }
    // The Workbench rows (Coverage at a glance) and their v0.1 sizes.
    const rows: Record<string, SizeId[]> = {
      service: ['glyph', 'small', 'wide'],
      metric: ['glyph', 'strip', 'small', 'wide'],
      check: ['glyph', 'strip', 'small', 'wide'],
      fleet: ['wide', 'tall', 'large', 'hero'],
      host: ['glyph', 'strip', 'small', 'wide', 'large'],
      guests: ['wide', 'tall', 'large'],
      containers: ['wide', 'tall', 'large'],
      unraid: ['glyph', 'strip', 'small', 'wide', 'tall', 'large', 'hero'],
      storage: ['glyph', 'strip', 'small', 'wide'],
      backups: ['strip', 'small', 'wide', 'large'],
      opnsense: ['glyph', 'strip', 'small', 'wide', 'large'],
      cameras: ['small', 'wide', 'large', 'hero'],
      homeassistant: ['glyph', 'strip', 'small', 'wide', 'large'],
      calendar: ['strip', 'wide', 'tall', 'large'],
      plex: ['glyph', 'strip', 'small', 'wide', 'large'],
      downloads: ['glyph', 'small', 'wide', 'tall'],
      bookmarks: ['glyph', 'small', 'wide', 'tall'],
      clock: ['glyph', 'strip', 'small', 'wide'],
      weather: ['glyph', 'strip', 'small', 'wide'],
    };
    for (const [type, sizes] of Object.entries(rows)) for (const s of sizes) expect(WIDGETS[type]!.sizes, `${type} ${s}`).toContain(s);
  });
});

describe('every widget at every size', () => {
  for (const [label, data] of [
    ['incidents, simulator shape', () => docs(withIncidents)],
    ['quiet home, hub shape', () => docs(healthy, 'hub')],
  ] as const) {
    for (const [type, spec] of Object.entries(WIDGETS)) {
      it(`${type}: its own layout per size (${label})`, () => {
        for (const size of spec.sizes) {
          const { tile, w } = mountWidget(data(), type, size);
          expect(tile().exists(), `${type} ${size}`).toBe(true);
          expect(tile().classes(), `${type} ${size}`).toContain(`s-${size}`);
          expect(tile().attributes('aria-label'), `${type} ${size}`).toBeTruthy();
          expect(tile().find('.skel').exists(), `${type} ${size} loading`).toBe(false);
          expect(w.text(), `${type} ${size}: ${w.text()}`).not.toMatch(BAD);
          w.unmount();
        }
      });
    }
  }

  it('shows skeletons, not zeros, before the first snapshot (the clock excepted)', () => {
    for (const [type, spec] of Object.entries(WIDGETS)) {
      const { tile, w } = mountWidget(undefined, type, spec.size, DEMO_TARGETS[type] ?? (type === 'service' ? 'app:sonarr' : undefined));
      if (type === 'clock') {
        expect(tile().find('time').exists()).toBe(true);
      } else {
        expect(tile().attributes('aria-busy'), type).toBe('true');
        expect(tile().find('.skel').exists(), type).toBe(true);
        expect(w.text(), type).not.toMatch(/\d%/);
      }
      w.unmount();
    }
  });

  const empty: Data = { graph: { resources: {}, relations: {} }, metrics: { series: {} }, inbox: { now: withIncidents.now, incidents: {} } };
  for (const [type, spec] of Object.entries(WIDGETS)) {
    it(`${type}: something helpful on an empty graph, never a fake 0`, () => {
      for (const size of spec.sizes) {
        const { w } = mountWidget(clone(empty), type, size);
        expect(w.text(), `${type} ${size}`).not.toMatch(BAD);
        expect(w.text(), `${type} ${size}: ${w.text()}`).not.toMatch(/(^|[^\d.])0(%| ?ms| ?Mb\/s)/);
        if (size !== 'glyph' && type !== 'clock' && type !== 'incidents') expect(w.text().length, `${type} ${size}`).toBeGreaterThan(8);
        w.unmount();
      }
    });
  }

  it('keeps last-known values hatched when the hub goes quiet, except the clock', async () => {
    for (const type of ['service', 'metric', 'check', 'unraid', 'storage', 'weather', 'clock']) {
      const { w, ws, live, tile } = mountWidget(docs(withIncidents), type, 'small');
      ws.drop();
      vi.advanceTimersByTime(4000);
      live.tick();
      await nextTick();
      expect(tile().classes().includes('is-stale'), type).toBe(type !== 'clock');
      w.unmount();
    }
  });
});

describe('service tile', () => {
  it('reads each integration’s stat by type: Sonarr queued and wanted, with its next release', () => {
    const { w, tile } = mountWidget(docs(withIncidents), 'service', 'small', 'app:sonarr');
    expect(w.find('.v').text()).toBe('14queued');
    expect(w.find('.lab').text()).toContain('31 wanted');
    expect(tile().classes()).toContain('is-attn');
    expect(w.find('.note').text()).toContain("Can't reach qBittorrent since 14:03");
    expect(w.find('.note').text()).toContain('likely: qBittorrent ran out of memory');
    expect(w.find('.tf').text()).toBe('on magpie');
  });

  it('says "Queue clear" for an empty queue, never a bare 0', () => {
    const { w } = mountWidget(docs(withIncidents), 'service', 'small', 'app:radarr');
    expect(w.find('.v').text()).toBe('Queue clear');
  });

  it('covers the specs and Tier 0 apps alike', () => {
    const cases: [string, string][] = [
      ['app:prowlarr', '1,448grabs'],
      ['app:traefik', '48routers'],
      ['app:uptimekuma', '12/14 up'],
      ['app:immich', '61.3kphotos'],
      ['app:mealie', '143recipes'],
      ['app:tautulli', '—playing'], // the Demo Home's Tautulli publishes no streams: unknown, never 0
      ['app:homeassistant', '4/4 home'],
      ['app:frigate', '4cameras'],
      ['app:qbittorrent', '0.9Mb/s ↓'],
      ['app:adguard', '15.0% blocked'],
      ['app:jellyfin', 'Nothing playing'],
      ['app:sabnzbd', '23.9Mb/s ↓'],
      ['app:seerr', '3pending'],
      ['app:audiobookshelf', 'Nobody listening'],
      ['app:linkding', '2,265bookmarks'],
      ['app:paperlessngx', '3,185documents'],
      ['app:nextcloud', '0active'], // last-known: it's down in maintenance mode
      ['app:speedtesttracker', '179Mb/s ↓'],
    ];
    for (const [id, text] of cases) {
      const { w } = mountWidget(docs(withIncidents), 'service', 'small', id);
      expect(w.find('.v').text(), id).toBe(text);
      w.unmount();
    }
  });

  it('shows Bazarr’s missing subtitles with the trend, and two facts when wide', () => {
    const { w } = mountWidget(docs(withIncidents), 'service', 'wide', 'app:bazarr');
    expect(w.find('.v').text()).toBe('617subtitles missing');
    expect(w.find('.sub').text()).toBe('609 episodes · 8 movies');
    expect(w.find('.spark').attributes('aria-label')).toContain('Bazarr');
  });

  it('keeps last-known values and the HTTP check when the integration needs repair', () => {
    const { w, tile } = mountWidget(docs(withIncidents), 'service', 'small', 'app:karakeep');
    expect(tile().classes()).toContain('is-stale');
    expect(tile().classes()).not.toContain('is-down');
    expect(w.find('.asof').text()).toBe('as of 09:04');
    expect(w.find('.v').text()).toBe('1,488bookmarks');
    expect(w.find('.note').text()).toContain("Karakeep's API changed. Values are last-known.");
    expect(w.find('.note button').text()).toBe('Reconnect');
    expect(w.find('.tf').text()).toBe('service up · 147 ms');
  });

  it('runs the repair in the Demo Home instead of asking for a token', async () => {
    const f = fakeFetch(engine().routes);
    const { w, ui } = mountWidget(docs(withIncidents), 'plex', 'wide', undefined, { demo: true });
    const button = w.find('.note button');
    expect(button.text()).toBe('Reconnect');
    expect(button.attributes('title')).toBe('Runs in the Demo Home');
    await button.trigger('click');
    await flushPromises();
    expect(f.calls.find((c) => c.path === '/api/actions/plan')?.body).toMatchObject({ action: 'integration.reconnect', target: 'integration:plex' });
    expect(ui.state.inboxOpen).toBe(false);
    expect(route.value.path).not.toMatch(/settings/);
  });

  it('tells other roles to ask an admin instead of offering Reconnect', () => {
    const { w } = mountWidget(docs(withIncidents), 'service', 'small', 'app:karakeep', { role: 'household' });
    expect(w.find('.note button').exists()).toBe(false);
    expect(w.find('.note').text()).toContain('Ask an admin to reconnect it.');
  });

  it('shows a withheld stat as "—" with a lock, never the value', () => {
    const d = docs(healthy);
    d.graph.resources['app:immich']!.attrs = { ...d.graph.resources['app:immich']!.attrs, withheld: ['photos'] };
    const { w } = mountWidget(d, 'service', 'small', 'app:immich');
    expect(w.find('.v').text()).toBe('—photos');
    expect(w.find('.note').text()).toContain('Ask an admin');
    expect(w.text()).not.toContain('44');
    expect(w.find('.tf').text()).toContain('service up');
  });

  it('says down with the likely cause, maintenance, and stopped on purpose', () => {
    const d = docs(withIncidents);
    setHealth(d, 'app:qbittorrent', { status: 'down', since: '2026-10-03T14:03:00Z', reason: 'exit 137' });
    let m = mountWidget(d, 'service', 'small', 'app:qbittorrent');
    expect(m.tile().classes()).toContain('is-down');
    expect(m.w.find('.note').text()).toBe('Down since 14:03 · exit 137 · likely: qBittorrent ran out of memory');
    // Its stats are last-known, so the header shows "as of"; the state word stays in the accessible name.
    expect(m.w.find('.asof').text()).toBe('as of 14:02');
    expect(m.tile().attributes('aria-label')).toContain('down since 14:03');
    m.w.unmount();

    const d2 = docs(healthy);
    setHealth(d2, 'app:mealie', { status: 'maintenance', reason: 'maintenance until 22:00' });
    m = mountWidget(d2, 'service', 'small', 'app:mealie');
    expect(m.tile().classes()).toContain('is-maint');
    expect(m.w.find('.note').text()).toBe('Paused · maintenance until 22:00');
    m.w.unmount();

    const d3 = docs(healthy);
    setHealth(d3, 'app:mealie', { status: 'ok' }, { state: 'stopped' });
    m = mountWidget(d3, 'service', 'small', 'app:mealie');
    expect(m.tile().classes()).toContain('is-off');
    expect(m.w.find('.note').text()).toBe('Stopped (as intended)');
  });

  it('opens the incident from the tile', async () => {
    const { tile, ui } = mountWidget(docs(withIncidents), 'service', 'small', 'app:sonarr');
    await tile().trigger('click');
    expect(ui.state.inboxFocus).toBe('qbittorrent-oom');
  });

  it('is a name and a value at glyph size', () => {
    const { w } = mountWidget(docs(withIncidents), 'service', 'glyph', 'app:prowlarr');
    expect(w.find('.gv').text()).toBe('1,448');
    expect(w.find('.gl').text()).toBe('grabs');
  });
});

describe('metric stat and sparkline', () => {
  it('is a value with unit, band and threshold; Ember past the threshold', () => {
    const { w, tile } = mountWidget(docs(withIncidents), 'metric', 'wide');
    expect(w.find('.v').text()).toBe('95%');
    expect(tile().classes()).toContain('is-attn');
    expect(w.find('.tg').text()).toMatch(/^memory 95% · no swap|^Memory 95%/);
    expect(w.find('.sub').text()).toContain('threshold 90%');
    expect(w.find('.spark').classes()).toContain('tone-attn');
    expect(w.find('.spark').attributes('aria-label')).toContain('threshold 90%');
    expect(w.find('.mg-thr').exists()).toBe(true);
  });

  it('is label · sparkline · value as a strip, and value plus unit as a glyph', () => {
    let m = mountWidget(docs(healthy), 'metric', 'strip', 'net:opnsense/wan#rx.mbps');
    expect(m.w.find('.spark').exists()).toBe(true);
    expect(m.w.find('.sv').text()).toMatch(/Mb\/s$/);
    m.w.unmount();
    m = mountWidget(docs(healthy), 'metric', 'glyph', 'pve:wren#cpu.pct');
    expect(m.w.find('.gv').text()).toMatch(/^\d+%$/);
    expect(m.w.find('.gl').text()).toBe('CPU');
  });

  it('says "No data yet" for a new series and names a missing one', () => {
    const d = docs(healthy);
    d.metrics.series['pve:wren#swap.pct'] = { value: null, history: [null, null], unit: '%' };
    let m = mountWidget(d, 'metric', 'small', 'pve:wren#swap.pct');
    expect(m.w.text()).toContain('No data yet · history starts now');
    m.w.unmount();
    m = mountWidget(docs(healthy), 'metric', 'small', 'pve:wren#nope.pct');
    expect(m.w.text()).toContain('Nothing reports nope pct here.');
  });

  it('keeps the last value when the source is down', () => {
    const d = docs(healthy);
    setHealth(d, 'pve:wren', { status: 'down', since: '2026-10-03T14:02:00Z' });
    const { w, tile } = mountWidget(d, 'metric', 'small', 'pve:wren#mem.pct');
    expect(tile().classes()).toContain('is-down');
    expect(w.find('.v').text()).toMatch(/^\d+%$/);
    expect(w.find('.note').text()).toBe('Source down since 14:02');
  });
});

describe('uptime check', () => {
  /** The Demo Home without its check resources: the apps' own http.ms are the fallback. */
  const withoutChecks = (d: Data): Data => {
    for (const r of Object.values(d.graph.resources)) if (r.kind === 'check') delete d.graph.resources[r.id];
    return d;
  };

  it('reads the Demo Home’s built-in checks, as a hub publishes them', () => {
    const { w } = mountWidget(docs(healthy), 'check', 'small');
    expect(w.find('.v').text()).toBe('2/2 up');
    const wide = mountWidget(docs(withIncidents), 'check', 'wide');
    expect(wide.w.findAll('.chk').map((r) => r.text())).toEqual([expect.stringContaining('Linkding'), expect.stringContaining('Magpie Unraid')]);
    // Unraid's web UI answers while its API integration can't be read.
    expect(wide.tile().classes()).not.toContain('is-down');
  });

  it('falls back to the apps’ http.ms without check resources', () => {
    const { w } = mountWidget(withoutChecks(docs(healthy)), 'check', 'small');
    expect(w.find('.v').text()).toBe('26/26 up');
    expect(w.findAll('.bars i')).toHaveLength(24);
    expect(w.find('.bars').attributes('aria-label')).toContain('Last 24 hours');
  });

  it('reads Uptime Kuma’s counts when it has no monitor resources', () => {
    const { w, tile } = mountWidget(docs(withIncidents), 'check', 'small', 'app:uptimekuma');
    expect(w.find('.v').text()).toBe('12/14 up');
    expect(tile().classes()).toContain('is-attn');
    expect(w.findAll('.bars i.b-down').length).toBeGreaterThan(0);
  });

  it('lists checks worst first when wide, each with its latency', () => {
    const d = withoutChecks(docs(healthy));
    d.metrics.series['app:mealie#http.ms']!.value = null;
    const { w, tile } = mountWidget(d, 'check', 'wide');
    expect(tile().classes()).toContain('is-down');
    const rows = w.findAll('.chk');
    expect(rows).toHaveLength(4);
    expect(rows[0]!.text()).toContain('Mealie');
    expect(rows[0]!.find('.g-down').exists()).toBe(true);
    expect(w.text()).toContain('+22 more');
  });

  it('says how to get checks when there are none', () => {
    const empty: Data = { graph: { resources: {}, relations: {} }, metrics: { series: {} }, inbox: { now: withIncidents.now, incidents: {} } };
    const { w } = mountWidget(empty, 'check', 'small');
    expect(w.text()).toContain('No checks yet');
    expect(w.text()).toContain('Every discovered URL gets one automatically.');
  });
});

describe('host card', () => {
  it('shows memory with its threshold, CPU and guests', () => {
    const { w, tile } = mountWidget(docs(withIncidents), 'host', 'small', 'pve:stork');
    expect(w.find('.v').text()).toBe('95% mem');
    expect(tile().classes()).toContain('is-attn');
    expect(w.find('.tf').text()).toContain('1/2 guests');
  });

  it('adds storage bars and the top guests when large', () => {
    const { w } = mountWidget(docs(withIncidents), 'host', 'large', 'pve:stork');
    expect(w.find('.host-disks').text()).toContain('tank');
    const guests = w.findAll('.list .li');
    // sandbox is down (internal-error), then the guest whose memory grew.
    expect(guests[0]!.text()).toContain('sandbox');
    expect(guests[0]!.text()).toContain('internal-error');
    expect(guests[1]!.text()).toContain('files');
  });

  it('dims a guest stopped on purpose', () => {
    const { w } = mountWidget(docs(healthy), 'host', 'large', 'pve:stork');
    const guests = w.findAll('.list .li');
    expect(guests[1]!.classes()).toContain('dim');
    expect(guests[1]!.text()).toContain('stopped (expected)');
  });

  it('says what goes down with a host, the router first among it', () => {
    const d = docs(withIncidents);
    setHealth(d, 'pve:wren', { status: 'down', since: '2026-10-03T14:02:00Z' }, { state: 'offline' });
    const { w, tile } = mountWidget(d, 'host', 'wide', 'pve:wren');
    expect(tile().classes()).toContain('is-down');
    expect(w.find('.note').text()).toBe('Unreachable since 14:02 · 3 guests, the router go with it');
  });

  it('is name and memory % at glyph, and a bar at strip', () => {
    let m = mountWidget(docs(withIncidents), 'host', 'glyph', 'pve:stork');
    expect(m.w.find('.gv').text()).toBe('95%');
    m.w.unmount();
    m = mountWidget(docs(withIncidents), 'host', 'strip', 'pve:stork');
    expect(m.w.find('[role="meter"]').attributes('aria-valuetext')).toBe('95%, threshold 90%');
  });
});

describe('guest list', () => {
  it('lists problems first and stopped-on-purpose guests dimmed', () => {
    const { w } = mountWidget(docs(withIncidents), 'guests', 'tall');
    const rows = w.findAll('.li');
    expect(rows[0]!.text()).toContain('sandbox'); // down: internal-error
    expect(rows[1]!.text()).toContain('files');
    expect(rows.some((r) => r.text().includes('homeassistant') && r.text().includes('backup failed'))).toBe(true);
    const off = rows.filter((r) => r.classes().includes('dim'));
    // homework is stopped on purpose, below everything running (win-games, also off, is past the fold)
    expect(off.map((r) => r.text())).toEqual([expect.stringContaining('homework')]);
    expect(rows.at(-1)!.classes()).toContain('dim');

    expect(off[0]!.find('.g-off').exists()).toBe(true);
  });

  it('shows a host’s guests as a table when large, and says when a host has none', () => {
    let m = mountWidget(docs(healthy), 'guests', 'large', 'pve:owl');
    expect(m.w.findAll('.sr:not(.head)')).toHaveLength(3);
    expect(m.w.find('.sr.head').text()).toContain('Backup');
    m.w.unmount();
    // Every Demo Home host runs something: empty egret by moving rook away.
    const bare = docs(healthy);
    delete bare.graph.resources['pve:egret/lxc/210'];
    for (const [k, r] of Object.entries(bare.graph.relations)) if (r.from === 'pve:egret/lxc/210' || r.to === 'pve:egret/lxc/210') delete bare.graph.relations[k];
    m = mountWidget(bare, 'guests', 'wide', 'pve:egret');
    expect(m.w.text()).toContain('No guests');
    expect(m.w.text()).toContain('Nothing runs on egret.');
  });

  it('shows a backup or migration in progress, and counts hidden guests', () => {
    const d = docs(healthy);
    d.graph.resources['pve:owl/qemu/302']!.attrs!['lock'] = 'backup';
    d.graph.resources['pve:owl']!.attrs!['hidden.guests'] = 3;
    const { w } = mountWidget(d, 'guests', 'wide', 'pve:owl');
    const busy = w.findAll('.li').find((r) => r.text().includes('studio'))!;
    expect(busy.find('.g-prog').exists()).toBe(true);
    expect(busy.text()).toContain('backing up');
    expect(w.find('.tf').text()).toContain('+3 hidden');
  });
});

describe('container list', () => {
  it('puts the unhealthy first with a glyph; healthy ones get no badge', () => {
    const { w } = mountWidget(docs(withIncidents), 'containers', 'tall');
    const rows = w.findAll('.li');
    expect(rows[0]!.text()).toContain('qbittorrent');
    expect(rows[0]!.find('.g-attn').exists()).toBe(true);
    expect(rows[1]!.find('.g').exists()).toBe(false);
  });

  it('is a table with image tags when large, scoped to one engine', () => {
    const { w } = mountWidget(docs(withIncidents), 'containers', 'large', 'docker:rook');
    expect(w.find('.tn').text()).toBe('Containers · rook');
    expect(w.findAll('.sr:not(.head)')).toHaveLength(9); // of rook's 15
    expect(w.text()).toContain('+6 more');
    expect(w.text()).toContain('0.31.0');
  });

  it('names what depends on a container that went down', () => {
    const d = docs(withIncidents);
    setHealth(d, 'docker:magpie/qbittorrent', { status: 'down', reason: 'OOM-killed' }, { state: 'exited' });
    d.graph.resources['docker:magpie/qbittorrent']!.attrs!['finished_at'] = '2026-10-03T14:03:00Z';
    const { w } = mountWidget(d, 'containers', 'wide');
    expect(w.find('.li').text()).toContain('exited (137) at 14:03 · OOM-killed · qBittorrent depends on it');
  });
});

describe('Unraid array', () => {
  it('re-lays out at every size: glyph, strip, wide, tall and hero', () => {
    let m = mountWidget(docs(withIncidents), 'unraid', 'glyph');
    expect(m.w.find('.gv').text()).toBe('71%');
    m.w.unmount();
    m = mountWidget(docs(withIncidents), 'unraid', 'strip');
    expect(m.w.find('.sx').text()).toBe('· 41°');
    expect(m.w.find('.sx [aria-label="parity valid"]').exists()).toBe(true);
    m.w.unmount();
    m = mountWidget(docs(withIncidents), 'unraid', 'wide');
    expect(m.w.find('.v').text()).toBe('71% of 84 TB');
    expect(m.w.find('.kv').text()).toContain('disk1 41°');
    m.w.unmount();
    m = mountWidget(docs(withIncidents), 'unraid', 'tall');
    expect(m.w.findAll('.disk')).toHaveLength(7);
    expect(m.w.find('.disk').text()).toContain('parity');
    m.w.unmount();
    m = mountWidget(docs(withIncidents), 'unraid', 'hero');
    expect(m.w.findAll('.dm')).toHaveLength(7);
    expect(m.w.find('.dm .bar').classes()).toContain('par');
    expect(m.w.find('.spark').attributes('aria-label')).toContain('Hottest disk');
  });

  it('shows a parity check in progress, not maintenance, and a disabled disk as down', () => {
    const d = docs(healthy);
    d.graph.resources['unraid:magpie/array']!.attrs!['parity'] = 'checking';
    d.graph.resources['unraid:magpie/array']!.attrs!['parity_check.progress'] = 40;
    let m = mountWidget(d, 'unraid', 'wide');
    expect(m.tile().classes()).not.toContain('is-maint');
    expect(m.w.find('.tg .g-prog').exists()).toBe(true);
    expect(m.w.find('.tg').text()).toContain('parity check · 40%');
    m.w.unmount();
    const d2 = docs(healthy);
    setHealth(d2, 'unraid:magpie/disk/disk3', { status: 'down', since: '2026-10-03T14:02:00Z' });
    m = mountWidget(d2, 'unraid', 'small');
    expect(m.tile().classes()).toContain('is-down');
    expect(m.w.find('.note').text()).toBe('disk3 disabled · parity protects data');
  });
});

describe('storage bar', () => {
  it('shows % used with its threshold tick and free space', () => {
    const { w } = mountWidget(docs(healthy), 'storage', 'strip', 'pve:owl/storage/local-lvm');
    expect(w.find('.sv').text()).toBe('64%');
    expect(w.find('.sx').text()).toBe('720 GB free');
    expect(w.find('[role="meter"]').attributes('aria-valuetext')).toBe('64%, threshold 85%');
  });

  it('is attention only over the threshold and climbing', () => {
    const { w, tile } = mountWidget(docs(withIncidents), 'storage', 'wide', 'pbs:main');
    expect(tile().classes()).toContain('is-attn');
    expect(w.find('.v').text()).toBe('92% used');
    expect(w.find('.mg-thr').exists()).toBe(true);
  });
});

describe('backups', () => {
  it('is "Backups · 92% · 1 failed" as a strip, and lists failed runs when large', () => {
    let m = mountWidget(docs(withIncidents), 'backups', 'strip');
    expect(m.w.find('.sv').text()).toBe('92%');
    expect(m.w.find('.sx').text()).toBe('1 failed');
    m.w.unmount();
    m = mountWidget(docs(withIncidents), 'backups', 'large');
    expect(m.w.find('.list').text()).toContain('aviary nightly');
    // wren's run couldn't reach PBS; homeassistant (aviary's run) and win-games (owl's) failed in theirs.
    expect(m.w.find('.nights').findAll('.bad')).toHaveLength(3);
    expect(m.w.find('.tf').text()).toContain('keep-daily 7');
  });
});

describe('router', () => {
  it('is WAN down now as a glyph, and says when the internet is down', () => {
    let m = mountWidget(docs(healthy), 'opnsense', 'glyph');
    expect(m.w.find('.gv').text()).toMatch(/↓$/);
    m.w.unmount();
    const d = docs(healthy);
    setHealth(d, 'net:opnsense/wan', { status: 'down', since: '2026-10-03T14:02:00Z' });
    m = mountWidget(d, 'opnsense', 'small');
    expect(m.tile().classes()).toContain('is-down');
    expect(m.w.find('.tg').text()).toContain('WAN down since 14:02');
    expect(m.w.find('.note').text()).toBe('Internet is down since 14:02');
  });

  it('adds interfaces and the gateways it can’t read when large', () => {
    const { w } = mountWidget(docs(healthy), 'opnsense', 'large');
    expect(w.find('.kv').text()).toContain('needs System: Gateways');
  });
});

describe('Camera Hero', () => {
  it('shows the latest detection at small and two cameras when wide', () => {
    let m = mountWidget(docs(withIncidents), 'cameras', 'small');
    expect(m.w.findAll('.cam')).toHaveLength(1);
    expect(m.w.find('.cam-cap').text()).toContain('Garage · car 73% · 14:02');
    m.w.unmount();
    m = mountWidget(docs(withIncidents), 'cameras', 'wide');
    expect(m.w.findAll('.cam')).toHaveLength(2);
  });

  it('is attention when one camera is offline, quiet for privacy mode, and frozen when Frigate is down', () => {
    const d = docs(healthy);
    setHealth(d, 'frigate:camera/garage', { status: 'down' }, { state: 'error' });
    let m = mountWidget(d, 'cameras', 'large');
    expect(m.tile().classes()).toContain('is-attn');
    expect(m.w.find('.tg').text()).toContain('Garage offline');
    m.w.unmount();
    const d2 = docs(healthy);
    // The garden camera: large shows three of the four, and the garage is fourth.
    setHealth(d2, 'frigate:camera/garden', { status: 'ok', reason: 'Turned off in Frigate' }, { state: 'stopped' });
    m = mountWidget(d2, 'cameras', 'large');
    expect(m.tile().classes()).not.toContain('is-attn');
    expect(m.w.text()).toContain('Garden · off (as intended)');
    m.w.unmount();
    const d3 = docs(healthy);
    setHealth(d3, 'app:frigate', { status: 'down', since: '2026-10-03T14:02:00Z' });
    m = mountWidget(d3, 'cameras', 'large');
    expect(m.tile().classes()).toEqual(expect.arrayContaining(['is-down', 'is-stale']));
    expect(m.w.find('.cam-rec').text()).toBe('LAST FRAME');
  });
});

describe('Home Assistant summary', () => {
  it('is people home as a glyph, home · lights · power as a strip', () => {
    let m = mountWidget(docs(healthy), 'homeassistant', 'glyph');
    expect(m.w.find('.gv').text()).toMatch(/^\d\/4$/);
    m.w.unmount();
    m = mountWidget(docs(healthy), 'homeassistant', 'strip');
    expect(m.w.find('.sx').text()).toMatch(/home · \d+ lights/);
    expect(m.w.find('.sv').text()).toMatch(/kW$/);
  });

  it('adds the counts as rows when large', () => {
    const { w } = mountWidget(docs(healthy), 'homeassistant', 'large');
    expect(w.find('.kv').text()).toContain('Lights on');
    expect(w.find('.kv').text()).toContain('of 52');
  });
});

describe('media calendar', () => {
  it('is the next release as a strip, and a week grid when large', () => {
    let m = mountWidget(docs(withIncidents), 'calendar', 'strip');
    expect(m.w.find('.sx').text()).toMatch(/^Next: /);
    m.w.unmount();
    m = mountWidget(docs(withIncidents), 'calendar', 'large');
    expect(m.w.findAll('.wk-day')).toHaveLength(7);
  });

  it('keeps the other instance’s rows when one is down, and says what’s missing', () => {
    const d = docs(withIncidents);
    setHealth(d, 'app:radarr', { status: 'down', since: '2026-10-03T14:02:00Z' });
    const { w, tile } = mountWidget(d, 'calendar', 'tall');
    expect(tile().classes()).not.toContain('is-down');
    expect(w.findAll('.ag-row').length).toBeGreaterThan(0);
    expect(w.find('.note').text()).toContain('Radarr down: movies missing from this list');
  });
});

describe('now playing', () => {
  it('keeps last-known counts and offers Reconnect when Plex rejects its token', () => {
    const { w, tile } = mountWidget(docs(withIncidents), 'plex', 'wide');
    expect(tile().classes()).toContain('is-stale');
    expect(w.find('.note').text()).toContain('Plex rejected its token. Values are last-known.');
    expect(w.find('.note button').text()).toBe('Reconnect');
  });

  it('is streams as a glyph and a sentence as a strip', () => {
    let m = mountWidget(docs(healthy), 'plex', 'glyph');
    expect(m.w.find('.gv').text()).toMatch(/^\d+$/);
    m.w.unmount();
    m = mountWidget(docs(healthy), 'plex', 'strip');
    expect(m.w.find('.sx').text()).toMatch(/playing/);
  });
});

describe('download queue', () => {
  it('lists qBittorrent and the *arr queues as rows', () => {
    const { w } = mountWidget(docs(withIncidents), 'downloads', 'tall');
    const rows = w.findAll('.li');
    expect(rows.map((r) => r.find('.t').text())).toEqual(['qBittorrent', 'SABnzbd', 'Sonarr', 'Radarr']);
    expect(rows[1]!.text()).toContain('3 in the queue');
    expect(rows[2]!.text()).toContain('14 queued');
    expect(rows[3]!.text()).toContain('queue clear');
  });

  it('names what waits on a client that’s down', () => {
    const d = docs(withIncidents);
    setHealth(d, 'app:qbittorrent', { status: 'down', since: '2026-10-03T14:03:00Z' });
    const { w } = mountWidget(d, 'downloads', 'wide');
    expect(w.find('.note').text()).toContain('Down since 14:03');
    expect(w.find('.note').text()).toContain('wait on it');
  });
});

describe('bookmarks', () => {
  it('lists discovered services as links that open in a new tab', () => {
    const { w } = mountWidget(docs(healthy), 'bookmarks', 'tall');
    const links = w.findAll('.bm a');
    expect(links).toHaveLength(10);
    expect(links[0]!.attributes('rel')).toBe('noopener noreferrer');
    expect(links[0]!.attributes('target')).toBe('_blank');
    expect(w.find('img').exists()).toBe(false);
  });

  it('marks a link whose check is down, and says when a sync breaks', () => {
    const d = docs(withIncidents);
    d.metrics.series['app:mealie#http.ms']!.value = null;
    const { w, tile } = mountWidget(d, 'bookmarks', 'wide', 'Apps');
    expect(tile().classes()).toContain('is-down');
    expect(w.find('.bm a.down').text()).toContain('Mealie');
    expect(w.find('.note').text()).toContain('Karakeep sync broken');
  });

  it('hides admin-only links from other roles and counts them', () => {
    const d = docs(healthy);
    d.graph.resources['app:cockpit']!.labels = { 'chadash.visibility': 'admin', 'chadash.group': 'Servers' };
    const { w } = mountWidget(d, 'bookmarks', 'small', 'Servers', { role: 'household' });
    expect(w.text()).not.toContain('Cockpit');
    expect(w.text()).toContain('1 links are for admins only.');
  });
});

describe('clock', () => {
  it('is hh:mm in the display zone, and a second zone when wide', () => {
    // The hub's clock: the incidents snapshot is at 14:05 (the quiet one at midnight).
    let m = mountWidget(docs(withIncidents), 'clock', 'glyph');
    expect(m.w.find('time').text()).toBe('14:05');
    m.w.unmount();
    m = mountWidget(docs(withIncidents), 'clock', 'wide', 'Asia/Tokyo');
    expect(m.w.find('.clock-date').text()).toBe('Saturday, 3 October 2026');
    expect(m.w.find('.tf').text()).toBe('Tokyo 23:05 · +9 h');
    expect(m.tile().attributes('aria-label')).toContain('Tokyo 23:05');
  });

  it('shows the Demo Home as local time, with real time in the second zone', () => {
    // The hub shows its 14:05 as 22:10 local (+08:05); the real time is 04:10 UTC on the 4th.
    vi.setSystemTime(new Date('2026-10-04T04:10:10Z'));
    setDisplayTimeZone('+08:05', 'MDT');
    const m = mountWidget(docs(withIncidents), 'clock', 'wide', 'Asia/Tokyo');
    expect(m.w.find('time').text()).toBe('22:10');
    expect(m.w.find('.clock-date').text()).toBe('Saturday, 3 October 2026');
    expect(m.w.find('.tf').text()).toMatch(/^Tokyo 13:10 · /);
    expect(absolute('2026-10-03T14:05:00Z')).toBe('3 Oct 2026, 22:10 MDT');
  });
});

describe('weather', () => {
  it('shows the Demo Home’s weather, credited to Open-Meteo', () => {
    const { w, tile } = mountWidget(docs(withIncidents), 'weather', 'wide');
    expect(tile().attributes('aria-label')).toMatch(/^Greenwich/);
    expect(w.text()).toMatch(/\d+°C/);
    expect(w.text()).toContain('Weather data by Open-Meteo.com');
  });

  it('says "Not connected" and where to set a location without a weather source', () => {
    const d = docs(healthy);
    delete d.graph.resources['weather:demo/here'];
    const { w, ui } = mountWidget(d, 'weather', 'small');
    expect(w.text()).toContain('Not connected. Set a location to see the weather here.');
    void ui;
    expect(w.find('button.linkish').text()).toBe('Set location');
  });
});

export type { Data, Incident };
