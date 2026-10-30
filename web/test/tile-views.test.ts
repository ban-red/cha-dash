/**
 * Tile views (widgets/views.ts, Tile.vue): which tiles offer them, paging by
 * the dots, the arrow keys and a swipe, remembering the pick per tile, and
 * accessibility on every view.
 */
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { defineComponent, h as el, nextTick } from 'vue';
import type { SizeId } from '@chadash/layout';
import WidgetHost from '../src/widgets/WidgetHost.vue';
import { preloadWidgets } from '../src/widgets/registry';
import { resetViews, VIEWS_KEY } from '../src/widgets/views';
import { setDisplayTimeZone } from '../src/lib/format';
import { axeViolations } from './axe';
import { docs, healthy, withIncidents } from './fixtures';
import { harness } from './harness';

beforeAll(() => preloadWidgets());

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'] });
  vi.setSystemTime(new Date('2026-10-03T14:05:10Z'));
  setDisplayTimeZone('UTC');
  localStorage.clear();
  resetViews();
});
afterEach(() => {
  vi.useRealTimers();
  document.body.innerHTML = '';
});

/** A tile in its page: always inside the front page's <main>. */
const InMain = defineComponent({
  props: { item: { type: Object, required: true }, size: { type: String, required: true } },
  setup: (props) => () => el('main', [el(WidgetHost, { item: props.item as { id: string; type: string }, size: props.size as SizeId })]),
});

function mountTile(type: string, size: SizeId, target?: string, data = docs(withIncidents)) {
  const h = harness(data);
  const id = target ? `${type}@${target}` : type;
  const w = h.mountIn(InMain, { item: { id, type }, size });
  const dots = () => w.findAll('.tile-views button');
  const pressed = () => dots().find((d) => d.attributes('aria-pressed') === 'true')?.attributes('aria-label');
  return { ...h, w, id, dots, pressed, tile: () => w.find('article.tile') };
}

async function swipe(el: Element, dx: number, pointerType = 'touch', dy = 0) {
  el.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, pointerId: 1, pointerType, isPrimary: true, clientX: 200, clientY: 100 }));
  el.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, pointerId: 1, pointerType, isPrimary: true, clientX: 200 + dx, clientY: 100 + dy }));
  el.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
  await nextTick();
}

describe('which tiles have views', () => {
  it('pages a small weather tile between now and the next hours', async () => {
    const { w, dots, pressed } = mountTile('weather', 'small');
    expect(dots().map((d) => d.attributes('aria-label'))).toEqual(['Now', 'Next hours']);
    expect(pressed()).toBe('Now');
    await dots()[1]!.trigger('click');
    expect(pressed()).toBe('Next hours');
    expect(w.find('.wx-rows').findAll('li').length).toBeGreaterThan(0);
    expect(w.find('.wx-now').exists()).toBe(false);
  });

  it('has no dots where the size already shows it all', () => {
    expect(mountTile('weather', 'wide').dots()).toHaveLength(0);
    expect(mountTile('host', 'large', 'pve:stork').dots()).toHaveLength(0);
    expect(mountTile('host', 'glyph', 'pve:stork').dots()).toHaveLength(0);
  });

  it('gives a host its guests and storage', async () => {
    const { w, dots, pressed } = mountTile('host', 'small', 'pve:stork');
    expect(dots().map((d) => d.attributes('aria-label'))).toEqual(['Overview', 'Guests', 'Storage']);
    await dots()[1]!.trigger('click');
    // sandbox is down, so it leads.
    expect(w.findAll('.list .li')[0]!.text()).toContain('sandbox');
    await dots()[2]!.trigger('click');
    expect(pressed()).toBe('Storage');
    expect(w.find('.host-disks').text()).toContain('tank');
  });

  it('gives a host that runs containers a Containers view', () => {
    const { dots } = mountTile('host', 'wide', 'unraid:magpie');
    expect(dots().map((d) => d.attributes('aria-label'))).toContain('Containers');
  });

  it('gives an *arr service tile what it expects next', async () => {
    const { w, dots } = mountTile('service', 'wide', 'app:sonarr');
    const labels = dots().map((d) => d.attributes('aria-label'));
    expect(labels).toContain('Up next');
    await dots()[labels.indexOf('Up next')]!.trigger('click');
    expect(w.findAll('.ag-row').length).toBeGreaterThan(0);
  });
});

describe('panel (4×3) and column (3×4)', () => {
  it('shows a guest list with more rows in a panel than a wide tile, and a plain list in a column', () => {
    const wide = mountTile('guests', 'wide', 'pve:stork').w.findAll('.li.g4').length;
    const panel = mountTile('guests', 'panel', 'pve:stork').w.findAll('.li.g4').length;
    expect(panel).toBeGreaterThanOrEqual(wide);
    expect(panel).toBeLessThanOrEqual(6);
    const col = mountTile('guests', 'column', 'pve:stork').w;
    expect(col.find('.li.g4').exists()).toBe(false);
    expect(col.findAll('.li').length).toBeGreaterThan(0);
  });

  it('offers them only where a widget declares them', async () => {
    const { WIDGETS } = await import('../src/model/widgets');
    expect(WIDGETS.guests!.sizes).toEqual(expect.arrayContaining(['panel', 'column']));
    expect(WIDGETS.clock!.sizes).not.toContain('panel');
    expect(WIDGETS.clock!.sizes).not.toContain('column');
  });
});

describe('Sonarr and Radarr', () => {
  const withLists = () => {
    const d = docs(withIncidents);
    d.graph.resources['app:sonarr']!.attrs = {
      ...d.graph.resources['app:sonarr']!.attrs,
      queue_items: [
        { title: 'Orbital Kitchen', detail: 'S02E01 · Re-entry', status: 'downloading', progress: 75, left_s: 750, client: 'qBittorrent' },
        { title: 'The Lighthouse Keepers', detail: 'S03E05', status: 'warning', progress: 100, error: 'No files found are eligible for import' },
      ],
      wanted_items: [
        { title: 'Quiet Valley', detail: 'S03E04 · Low Tide', at: Date.parse('2026-10-02T22:00:00Z') / 1000 },
        { title: 'Orbital Kitchen', detail: 'S02E09' },
      ],
    };
    return d;
  };

  it('pages a small tile between now, what is queued and what is wanted', async () => {
    const { w, dots, pressed } = mountTile('arr', 'small', 'app:sonarr', withLists());
    expect(dots().map((d) => d.attributes('aria-label'))).toEqual(['Now', 'Queue', 'Wanted']);
    await dots()[1]!.trigger('click');
    expect(pressed()).toBe('Queue');
    const q = w.findAll('.arr-list .ag-row');
    expect(q).toHaveLength(2);
    expect(q[0]!.text()).toContain('Orbital Kitchen');
    expect(q[0]!.text()).toContain('75%');
    expect(q[0]!.text()).toContain('12m left');
    expect(q[1]!.classes()).toContain('bad');
    expect(q[1]!.text()).toContain('No files found are eligible for import');
    await dots()[2]!.trigger('click');
    const wr = w.findAll('.arr-list .ag-row');
    expect(wr).toHaveLength(2);
    expect(wr[0]!.text()).toContain('Quiet Valley');
    expect(wr[0]!.text()).toContain('S03E04 · Low Tide');
    expect(wr[1]!.text()).toContain('—');
    expect(w.text()).not.toMatch(/undefined|NaN|\[object/);
  });

  it('has only the views it has data for', () => {
    expect(mountTile('arr', 'small', 'app:sonarr').dots()).toHaveLength(0);
  });

  it('passes axe on both lists', async () => {
    vi.useRealTimers();
    for (const size of ['small', 'wide'] as const) {
      const { w, dots } = mountTile('arr', size, 'app:sonarr', withLists());
      for (const i of [1, 2]) {
        await dots()[i]!.trigger('click');
        expect(await axeViolations(), `${size} view ${i}`).toEqual([]);
      }
      w.unmount();
    }
  });
});

describe('qBittorrent', () => {
  const withTorrents = () => {
    const d = docs(withIncidents);
    d.graph.resources['app:qbittorrent']!.attrs = {
      ...d.graph.resources['app:qbittorrent']!.attrs,
      active: [
        { name: 'debian-13.1.0-amd64-netinst.iso', state: 'downloading', progress: 62, dl_mbps: 15.2, ul_mbps: 0, eta_s: 5400 },
        { name: 'archlinux-2026.10.01-x86_64.iso', state: 'stalled', progress: 18, dl_mbps: 0, ul_mbps: 0 },
        { name: 'Sintel (2010) [2160p]', state: 'seeding', progress: 100, dl_mbps: 0, ul_mbps: 5.1 },
      ],
      list: [
        { name: 'old-backup-archive', state: 'error', progress: 100, dl_mbps: 0, ul_mbps: 0, ratio: 2.6, added: 400 },
        { name: 'debian-13.1.0-amd64-netinst.iso', state: 'downloading', progress: 62, dl_mbps: 15.2, ul_mbps: 0, ratio: 0, added: 300 },
        { name: 'Sintel (2010) [2160p]', state: 'seeding', progress: 100, dl_mbps: 0, ul_mbps: 5.1, ratio: 1.8, added: 200 },
        { name: 'Tears of Steel (2012) [1080p]', state: 'seeding', progress: 100, dl_mbps: 0, ul_mbps: 0, ratio: 1.9, added: 100 },
      ],
      torrents: 12,
      ratio: 1.84,
      free_gib: 6144,
      peers: 88,
    };
    return d;
  };

  it('lists the torrents beside the speed on a wide tile, with the facts underneath', () => {
    const { w } = mountTile('downloads', 'wide', 'app:qbittorrent', withTorrents());
    const rows = w.findAll('.dl-beside .ag-row');
    expect(rows).toHaveLength(2);
    expect(rows[0]!.text()).toContain('debian-13.1.0-amd64-netinst.iso');
    expect(rows[0]!.text()).toContain('62%');
    expect(rows[0]!.text()).toContain('1h 30m left');
    expect(w.find('.dl-beside .more').text()).toBe('+1 more');
    expect(w.find('.dl-facts').text()).toContain('ratio 1.84');
    expect(w.find('.dl-facts').text()).toContain('6 TiB free');
    expect(w.text()).not.toMatch(/undefined|NaN|\[object/);
  });

  it('lists more of them under the speed on a panel, uploads last', () => {
    const { w } = mountTile('downloads', 'panel', 'app:qbittorrent', withTorrents());
    const rows = w.findAll('.dl-torrents .ag-row');
    expect(rows).toHaveLength(3);
    expect(rows[2]!.text()).toContain('↑ 5.1 Mb/s');
    expect(w.find('.dl-beside').exists()).toBe(false);
  });

  it('pages to a compact table of every torrent with its status', async () => {
    const { w, dots, pressed } = mountTile('downloads', 'wide', 'app:qbittorrent', withTorrents());
    expect(dots().map((d) => d.attributes('aria-label'))).toEqual(['Now', 'Torrents']);
    await dots()[1]!.trigger('click');
    expect(pressed()).toBe('Torrents');
    const rows = w.findAll('.dl-tr');
    expect(rows).toHaveLength(4);
    expect(rows[0]!.text()).toContain('old-backup-archive');
    expect(rows[0]!.text()).toContain('error');
    expect(rows[1]!.text()).toContain('62%');
    expect(rows[1]!.text()).toContain('15.2 ↓');
    expect(rows[3]!.text()).toContain('1.9×');
    expect(w.find('.dl-more').text()).toBe('+8 more not listed');
    expect(w.text()).not.toMatch(/undefined|NaN|\[object/);
  });

  it('filters and sorts the table', async () => {
    const { w, dots } = mountTile('downloads', 'wide', 'app:qbittorrent', withTorrents());
    await dots()[1]!.trigger('click');
    const [filter, sort] = w.findAll('select.dl-sel');
    expect(filter!.findAll('option').map((o) => o.text())).toEqual(['All (4)', 'Downloading (1)', 'Seeding (2)', 'Errors (1)']);
    expect((sort!.element as HTMLSelectElement).value).toBe('added'); // newest first by default
    await filter!.setValue('seeding');
    expect(w.findAll('.dl-tr').map((r) => r.find('.dl-name').text())).toEqual(['Sintel (2010) [2160p]', 'Tears of Steel (2012) [1080p]']);
    expect(w.find('.dl-more').exists()).toBe(false);
    await filter!.setValue('all');
    await sort!.setValue('name');
    expect(w.findAll('.dl-tr')[0]!.find('.dl-name').text()).toBe('debian-13.1.0-amd64-netinst.iso');
    await sort!.setValue('ratio');
    expect(w.findAll('.dl-tr')[0]!.find('.dl-name').text()).toBe('old-backup-archive');
    expect(w.find('.dl-table').attributes('tabindex')).toBe('0');
  });

  it('fills a 4×4 with the torrents, and pages to the table', async () => {
    const { w, dots } = mountTile('downloads', 'large', 'app:qbittorrent', withTorrents());
    expect(dots().map((d) => d.attributes('aria-label'))).toEqual(['Now', 'Torrents']);
    expect(w.findAll('.dl-torrents .ag-row')).toHaveLength(3);
    expect(w.find('.dl-facts').exists()).toBe(true);
    await dots()[1]!.trigger('click');
    expect(w.findAll('.dl-tr')).toHaveLength(4);
  });

  it('has no Torrents view without a list', () => {
    expect(mountTile('downloads', 'wide', 'app:qbittorrent').dots()).toHaveLength(0);
  });

  it('still lists the queues without torrent data', () => {
    const { w } = mountTile('downloads', 'wide', 'app:qbittorrent');
    expect(w.find('.dl-torrents').exists()).toBe(false);
    expect(w.find('ul[aria-label="Queues"]').exists()).toBe(true);
  });

  it('passes axe', async () => {
    vi.useRealTimers();
    for (const size of ['wide', 'panel', 'tall', 'large'] as const) {
      const { w, dots } = mountTile('downloads', size, 'app:qbittorrent', withTorrents());
      expect(await axeViolations(), size).toEqual([]);
      if (dots().length) {
        await dots()[1]!.trigger('click');
        expect(await axeViolations(), `${size} table`).toEqual([]);
      }
      w.unmount();
    }
  });
});

describe('Tautulli', () => {
  const withPlays = () => {
    const d = docs(withIncidents);
    d.graph.resources['app:tautulli']!.attrs = {
      ...d.graph.resources['app:tautulli']!.attrs,
      now_playing: [
        { title: 'Big Buck Bunny', user: 'Lab Guest', state: 'playing', progress: 64, decision: 'transcode' },
        { title: 'Example Show - Pilot', user: 'Lab Member', state: 'paused', progress: 10, decision: 'direct play' },
        { title: 'A Third Film', user: 'Lab Member', state: 'playing', progress: 5, decision: 'direct play' },
      ],
      history: [
        { title: 'Example Show - Pilot', user: 'Lab Member', at: Date.parse('2026-10-03T09:00:00Z') / 1000, percent: 98, watched: true },
        { title: 'Big Buck Bunny', user: 'Lab Guest', at: Date.parse('2026-10-02T20:30:00Z') / 1000, percent: 41, watched: false },
      ],
    };
    return d;
  };

  it('lists what is playing, and pages to a viewing history', async () => {
    const { w, dots, pressed } = mountTile('service', 'small', 'app:tautulli', withPlays());
    expect(dots().map((d) => d.attributes('aria-label'))).toEqual(['Now', 'History']);
    const rows = w.findAll('.svc-playing .ag-row');
    expect(rows).toHaveLength(2);
    expect(rows[0]!.text()).toContain('Big Buck Bunny');
    expect(rows[0]!.text()).toContain('64%');
    expect(rows[1]!.text()).toContain('paused');
    expect(w.find('.svc-more').text()).toBe('+1 more');
    await dots()[1]!.trigger('click');
    expect(pressed()).toBe('History');
    const hist = w.findAll('.ag-row');
    expect(hist).toHaveLength(2);
    expect(hist[0]!.text()).toContain('Example Show - Pilot');
    expect(hist[0]!.text()).toContain('Today');
    expect(hist[1]!.text()).toContain('stopped at 41%');
    expect(w.text()).not.toMatch(/undefined|NaN|\[object/);
  });

  it('lists what is playing beside the stat on a wide tile, not under it', () => {
    const { w } = mountTile('service', 'wide', 'app:tautulli', withPlays());
    expect(w.find('.split .svc-playing').exists()).toBe(true);
    expect(w.findAll('.svc-playing .ag-row')).toHaveLength(2);
    expect(w.find('.mg').exists()).toBe(false);
  });

  it('has no History view without history, and no list without sessions', () => {
    const { w, dots } = mountTile('service', 'small', 'app:tautulli');
    expect(dots().map((d) => d.attributes('aria-label'))).not.toContain('History');
    expect(w.find('.svc-playing').exists()).toBe(false);
  });

  it('passes axe on both views', async () => {
    vi.useRealTimers();
    for (const size of ['small', 'wide'] as const) {
      const { w, dots } = mountTile('service', size, 'app:tautulli', withPlays());
      expect(await axeViolations(), `${size} now`).toEqual([]);
      await dots()[1]!.trigger('click');
      expect(await axeViolations(), `${size} history`).toEqual([]);
      w.unmount();
    }
  });
});

describe('Now playing on Plex', () => {
  // The healthy snapshot's clock is 2026-10-03 00:00 UTC.
  const at = (iso: string) => Date.parse(iso) / 1000;
  const withPlex = (playing = true) => {
    const d = docs(healthy);
    const streams = d.metrics.series['app:plex#streams']!;
    d.metrics.series['app:plex#streams'] = { ...streams, value: playing ? 3 : 0 };
    d.metrics.series['app:plex#streams.transcode'] = { ...streams, value: playing ? 1 : 0 };
    d.metrics.series['app:plex#bandwidth.mbps'] = { ...streams, value: playing ? 16 : 0, unit: 'Mb/s' };
    d.graph.resources['app:plex']!.attrs = {
      ...d.graph.resources['app:plex']!.attrs,
      episodes: 14212,
      artists: 412,
      streams_remote: 1,
      now_playing: playing
        ? [
            { title: 'Example Show - Pilot', kind: 'episode', user: 'Lab Member', player: 'Living room TV', state: 'playing', progress: 44, decision: 'direct play', remote: false },
            { title: 'Big Buck Bunny', kind: 'movie', user: 'Lab Guest', player: 'Phone', state: 'paused', progress: 5, decision: 'transcode', remote: true },
            { title: 'A Third Film', kind: 'movie', state: 'playing', progress: 80, decision: 'direct play', remote: false },
          ]
        : [],
      recently_added: [
        { kind: 'movie', title: 'Tears of Steel', detail: '2012', library: 'Movies', at: at('2026-10-02T23:00:00Z') },
        { kind: 'season', title: 'Example Show', detail: 'Season 2', library: 'TV Shows', at: at('2026-10-02T19:00:00Z') },
        { kind: 'album', title: 'Open Water', detail: 'The Synthetic Quartet', library: 'Music', at: at('2026-10-01T00:00:00Z') },
      ],
    };
    return d;
  };

  it('lists what is playing beside the count on a wide tile, and pages to recently added', async () => {
    const { w, dots, pressed } = mountTile('plex', 'wide', undefined, withPlex());
    expect(dots().map((d) => d.attributes('aria-label'))).toEqual(['Now', 'Recently added']);
    const rows = w.findAll('.split [role="listitem"]');
    expect(rows).toHaveLength(2);
    expect(rows[0]!.text()).toContain('44%');
    expect(rows[0]!.text()).toContain('Example Show - Pilot');
    expect(rows[0]!.text()).toContain('Lab Member · Living room TV · direct play');
    expect(rows[1]!.text()).toContain('paused');
    expect(w.find('.px-more').text()).toBe('+1 more');
    expect(w.find('.split .sub').text()).toBe('1 transcoding · 16 Mb/s');
    await dots()[1]!.trigger('click');
    expect(pressed()).toBe('Recently added');
    const added = w.findAll('[role="listitem"]');
    expect(added.map((r) => r.text())).toEqual(['1h agoTears of Steel2012 · Movies', '5h agoExample ShowSeason 2 · TV Shows', '2d agoOpen WaterThe Synthetic Quartet · Music']);
    expect(w.text()).not.toMatch(/undefined|NaN|\[object/);
  });

  it('shows the library beside the count when nothing plays', () => {
    const { w } = mountTile('plex', 'wide', undefined, withPlex(false));
    expect(w.find('.split [role="list"]').exists()).toBe(false);
    expect(w.text()).toContain('14,212 episodes · 412 artists');
    expect(w.text()).toContain('Nothing playing');
  });

  it('names the first title on a small tile', () => {
    const { w } = mountTile('plex', 'small', undefined, withPlex());
    expect(w.find('.v').text()).toBe('3playing');
    expect(w.find('.lab').text()).toBe('Example Show - Pilot +2');
  });

  it('shows playing, recently added and the trend together on a 4×4', () => {
    const { w, dots } = mountTile('plex', 'large', undefined, withPlex());
    expect(dots()).toHaveLength(0);
    expect(w.find('[aria-label="Recently added to Plex"]').exists()).toBe(true);
    expect(w.find('.mg').exists()).toBe(true);
    expect(w.find('[aria-label="Playing on Plex"]').findAll('[role="listitem"]')).toHaveLength(3);
    expect(w.find('.tf').text()).toBe('1 of 3 remote');
    expect(w.text()).not.toMatch(/undefined|NaN|\[object|arrive when/);
  });

  it('has no Recently added view without the list', () => {
    expect(mountTile('plex', 'wide').dots()).toHaveLength(0);
  });

  it('passes axe on both views and the 4×4', async () => {
    vi.useRealTimers();
    for (const size of ['small', 'wide'] as const) {
      const { w, dots } = mountTile('plex', size, undefined, withPlex());
      expect(await axeViolations(), `${size} now`).toEqual([]);
      await dots()[1]!.trigger('click');
      expect(await axeViolations(), `${size} recent`).toEqual([]);
      w.unmount();
    }
    mountTile('plex', 'large', undefined, withPlex());
    expect(await axeViolations(), 'large').toEqual([]);
  });
});

describe('paging', () => {
  it('moves with the arrow keys on the dots, wrapping, and keeps focus on the pressed dot', async () => {
    const { dots, pressed } = mountTile('host', 'small', 'pve:stork');
    await dots()[0]!.trigger('keydown', { key: 'ArrowLeft' });
    expect(pressed()).toBe('Storage');
    await nextTick();
    expect(document.activeElement).toBe(dots()[2]!.element);
    await dots()[2]!.trigger('keydown', { key: 'Home' });
    expect(pressed()).toBe('Overview');
    expect(dots().map((d) => d.attributes('tabindex'))).toEqual(['0', '-1', '-1']);
  });

  it('swipes on touch, and swallows the click that ends the swipe', async () => {
    const { tile, pressed, ui } = mountTile('host', 'small', 'pve:stork');
    const open = vi.spyOn(ui, 'openInbox');
    await swipe(tile().element, -80);
    expect(pressed()).toBe('Guests');
    expect(tile().classes()).toContain('slide-next');
    await swipe(tile().element, 80);
    expect(pressed()).toBe('Overview');
    expect(tile().classes()).toContain('slide-prev');
    expect(open).not.toHaveBeenCalled();
  });

  it('ignores a mouse drag, a short move and a mostly vertical one', async () => {
    const { tile, pressed } = mountTile('host', 'small', 'pve:stork');
    await swipe(tile().element, -80, 'mouse');
    await swipe(tile().element, -20);
    await swipe(tile().element, -60, 'touch', 120);
    expect(pressed()).toBe('Overview');
  });

  it("doesn't swipe in edit mode", async () => {
    const { tile, pressed, ui } = mountTile('host', 'small', 'pve:stork');
    ui.state.editing = true;
    await swipe(tile().element, -80);
    expect(pressed()).toBe('Overview');
  });

  it('names the view in the tile’s label', async () => {
    const { tile, dots } = mountTile('weather', 'small');
    await dots()[1]!.trigger('click');
    expect(tile().attributes('aria-label')).toMatch(/Next hours$/);
  });
});

describe('remembering the view', () => {
  it('keeps the pick per tile on this device', async () => {
    const first = mountTile('host', 'small', 'pve:stork');
    await first.dots()[2]!.trigger('click');
    expect(JSON.parse(localStorage.getItem(VIEWS_KEY) ?? '{}')).toEqual({ [first.id]: 'storage' });
    first.w.unmount();
    expect(mountTile('host', 'small', 'pve:stork').pressed()).toBe('Storage');
    // Another tile isn't affected.
    expect(mountTile('host', 'small', 'pve:wren').pressed()).toBe('Overview');
  });

  it('falls back to the first view when the remembered one is gone', async () => {
    const first = mountTile('weather', 'small');
    await first.dots()[1]!.trigger('click');
    first.w.unmount();
    // The forecast went away: one view left, so no dots, and it's "now".
    const d = docs(withIncidents);
    delete (d.graph.resources['weather:demo/here']!.attrs as Record<string, unknown>)['hourly'];
    const again = mountTile('weather', 'small', undefined, d);
    expect(again.dots()).toHaveLength(0);
    expect(again.w.find('.wx-now').exists()).toBe(true);
  });
});

describe('accessibility', () => {
  it('passes axe on every view', async () => {
    vi.useRealTimers();
    for (const [type, size, target] of [
      ['host', 'small', 'pve:stork'],
      ['host', 'wide', 'unraid:magpie'],
      ['weather', 'small', undefined],
      ['service', 'small', 'app:sonarr'],
    ] as const) {
      const { w, dots } = mountTile(type, size, target);
      for (let i = 0; i < dots().length; i++) {
        await dots()[i]!.trigger('click');
        expect(await axeViolations(), `${type} ${size} view ${i}`).toEqual([]);
      }
      w.unmount();
    }
  });
});
