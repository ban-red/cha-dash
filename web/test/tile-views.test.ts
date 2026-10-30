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
import { docs, withIncidents } from './fixtures';
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
