/**
 * The Cameras tile (widgets/CamerasWidget.vue) with its widget settings
 * (widgets/options.ts): pictures through the hub when Frigate shares them,
 * the camera picks, opening a camera's sheet, and the control row.
 */
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { computed, defineComponent, h as el, nextTick } from 'vue';
import { flushPromises } from '@vue/test-utils';
import type { SizeId } from '@chadash/layout';
import CamerasWidget from '../src/widgets/CamerasWidget.vue';
import { optionValue } from '../src/widgets/options';
import { WIDGETS } from '../src/model/widgets';
import { setDisplayTimeZone } from '../src/lib/format';
import { provideTile, type TileConfig } from '../src/stores/tiles';
import { axeViolations } from './axe';
import { docs, healthy } from './fixtures';
import { fakeFetch } from './fake-fetch';
import { harness } from './harness';

/** Images "load" at once, and the tile's requests are kept. */
const loaded: string[] = [];
class FakeImage {
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  #src = '';
  get src() {
    return this.#src;
  }
  set src(v: string) {
    this.#src = v;
    loaded.push(v);
    queueMicrotask(() => (v.includes('broken') ? this.onerror?.() : this.onload?.()));
  }
}

beforeAll(() => setDisplayTimeZone('UTC'));
beforeEach(() => {
  loaded.length = 0;
  vi.stubGlobal('Image', FakeImage);
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
  document.body.innerHTML = '';
});

const CAMS = ['frigate:camera/driveway', 'frigate:camera/front_door', 'frigate:camera/garden', 'frigate:camera/garage'];

/** The Demo Home, with Frigate sharing pictures and reporting every switch. */
function house(pictures = true) {
  const d = docs(healthy);
  for (const id of CAMS) {
    const r = d.graph.resources[id]!;
    r.attrs = { ...r.attrs, picture: pictures, enabled: true, snapshots: true, alerts: false };
  }
  return d;
}

const offers = (available = true) =>
  ['detect', 'record', 'snapshots', 'alerts', 'enabled'].flatMap((f) =>
    ['on', 'off'].map((dir) => ({
      id: `frigate.camera.${f}.${dir}`,
      label: `Turn ${f} ${dir}`,
      tier: 'T1',
      available,
      ...(available ? {} : { unavailable: 'Add an admin account under Control in the Frigate settings.' }),
    })),
  );

function mountCams(size: SizeId, config?: TileConfig, data = house(), target = 'app:frigate') {
  const hs = harness(data);
  const Host = defineComponent({
    setup() {
      provideTile({ itemId: 'cameras@app:frigate', type: 'cameras', target, config: computed(() => config) });
      return () => el('main', [el(CamerasWidget, { size, target })]);
    },
  });
  const w = hs.mountIn(Host);
  return { ...hs, w };
}

describe('widget options', () => {
  it('falls back to the default for a missing or malformed value', () => {
    const [cams, pics, refresh] = WIDGETS['cameras']!.options!;
    expect(optionValue(pics!, undefined)).toBe(true);
    expect(optionValue(pics!, 'yes')).toBe(true);
    expect(optionValue(pics!, false)).toBe(false);
    expect(optionValue(refresh!, '30')).toBe('30');
    expect(optionValue(refresh!, '7')).toBe('10');
    expect(optionValue(cams!, ['a', 3, 'b'])).toEqual(['a', 'b']);
    expect(optionValue(cams!, 'a')).toEqual([]);
  });
});

describe('Cameras', () => {
  it('shows each camera’s picture through the hub when Frigate shares them', async () => {
    fakeFetch({ 'GET /api/actions': { status: 200, body: [] } });
    const { w } = mountCams('large');
    await flushPromises();
    const imgs = w.findAll('.cam img');
    expect(imgs).toHaveLength(3);
    const url = new URL(imgs[0]!.attributes('src')!, 'http://hub.test');
    expect(url.pathname).toBe('/api/integrations/instances/frigate/picture');
    expect(url.searchParams.get('resource')).toBe('frigate:camera/driveway');
    expect(url.searchParams.get('h')).toBe('540');
    expect(w.find('.cam.big').classes()).toContain('pic');
  });

  it('keeps the drawn placeholder when pictures are off, in Frigate or in the tile', async () => {
    fakeFetch({ 'GET /api/actions': { status: 200, body: [] } });
    let m = mountCams('wide', undefined, house(false));
    await flushPromises();
    expect(m.w.find('.cam img').exists()).toBe(false);
    expect(m.w.findAll('.cam svg.scene')).toHaveLength(2);
    m.w.unmount();
    m = mountCams('wide', { options: { pictures: false } });
    await flushPromises();
    expect(m.w.find('.cam img').exists()).toBe(false);
    expect(loaded).toEqual([]);
  });

  it('falls back to the placeholder when a picture fails', async () => {
    fakeFetch({ 'GET /api/actions': { status: 200, body: [] } });
    const d = house();
    d.graph.resources['frigate:camera/driveway']!.source = 'integration:broken';
    const { w } = mountCams('small', undefined, d);
    await flushPromises();
    // Small shows the newest detection: the garden.
    expect(w.find('.cam img').exists()).toBe(true);
    const w2 = mountCams('wide', undefined, d).w;
    await flushPromises();
    expect(w2.findAll('.cam')[0]!.find('img').exists()).toBe(false);
    expect(w2.findAll('.cam')[0]!.find('svg.scene').exists()).toBe(true);
  });

  it('refreshes on the tile’s interval', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    // No IntersectionObserver: the picture counts as on screen, as a real one reports at once.
    vi.stubGlobal('IntersectionObserver', undefined);
    fakeFetch({ 'GET /api/actions': { status: 200, body: [] } });
    mountCams('small', { options: { refresh: '5' } });
    await flushPromises();
    const first = loaded.length;
    expect(first).toBe(1);
    vi.advanceTimersByTime(5000);
    await flushPromises();
    expect(loaded.length).toBe(2);
  });

  it('shows only the cameras picked in its settings', async () => {
    fakeFetch({ 'GET /api/actions': { status: 200, body: [] } });
    const { w } = mountCams('large', { options: { cameras: ['frigate:camera/garden', 'frigate:camera/garage'] } });
    await flushPromises();
    const names = w.findAll('.cam .cam-ts').map((x) => x.text());
    expect(names).toEqual([expect.stringMatching(/^GARDEN/), expect.stringMatching(/^GARAGE/)]);
    expect(w.find('.tf').text()).toContain('2 cameras');
  });

  it('opens a camera’s sheet from its frame', async () => {
    fakeFetch({ 'GET /api/actions': { status: 200, body: [] } });
    const { w, ui } = mountCams('wide');
    await flushPromises();
    const frame = w.find('button.cam');
    expect(frame.exists()).toBe(true);
    await frame.trigger('click');
    expect(ui.sheet.value).toBe('frigate:camera/driveway');
  });

  it('offers the main camera’s switches, and runs the one pressed', async () => {
    const hub = fakeFetch({
      'GET /api/actions': { status: 200, body: offers() },
      'POST /api/actions/plan': { status: 200, body: { id: 'p1', hash: 'h', action: 'frigate.camera.detect.off', label: 'Turn detect off', target: 'frigate:camera/driveway', tier: 'T1', confirm: 'none', step_up: 'none' } },
      'POST /api/plans/p1/approve': { status: 202, body: { task: { id: 't1', action: 'frigate.camera.detect.off', target: 'frigate:camera/driveway', state: 'running' } } },
    });
    const { w } = mountCams('large');
    await flushPromises();
    expect(hub.calls.some((c) => c.path === `/api/actions?target=${encodeURIComponent('frigate:camera/driveway')}`)).toBe(true);
    const sw = w.findAll('.cam-sw');
    expect(sw.map((b) => [b.text(), b.attributes('aria-pressed')])).toEqual([
      ['Detection', 'true'],
      ['Recording', 'true'],
      ['Snapshots', 'true'],
      ['Alerts', 'false'],
      ['Camera', 'true'],
    ]);
    await sw[0]!.trigger('click');
    await flushPromises();
    expect(hub.last('POST', '/api/actions/plan')?.body).toMatchObject({ action: 'frigate.camera.detect.off', target: 'frigate:camera/driveway' });
  });

  it('says why the switches can’t be used without a control account', async () => {
    fakeFetch({ 'GET /api/actions': { status: 200, body: offers(false) } });
    const { w } = mountCams('large');
    await flushPromises();
    expect(w.findAll('.cam-sw').every((b) => b.attributes('disabled') !== undefined)).toBe(true);
    expect(w.find('.cam-why').text()).toBe('Add an admin account under Control in the Frigate settings.');
  });

  it('has no switches when turned off in its settings, on a wide tile, or without offers', async () => {
    fakeFetch({ 'GET /api/actions': { status: 200, body: offers() } });
    expect((await (async () => { const m = mountCams('large', { options: { controls: false } }); await flushPromises(); return m.w; })()).find('.cam-ctl').exists()).toBe(false);
    const wide = mountCams('wide').w;
    await flushPromises();
    expect(wide.find('.cam-ctl').exists()).toBe(false);
    fakeFetch({ 'GET /api/actions': { status: 200, body: [] } });
    const none = mountCams('large').w;
    await flushPromises();
    expect(none.find('.cam-ctl').exists()).toBe(false);
  });

  it('hides the detections list when turned off', async () => {
    fakeFetch({ 'GET /api/actions': { status: 200, body: [] } });
    const on = mountCams('hero').w;
    await flushPromises();
    expect(on.find('.cam-events').exists()).toBe(true);
    const off = mountCams('hero', { options: { detections: false } }).w;
    await flushPromises();
    expect(off.find('.cam-events').exists()).toBe(false);
  });

  it('passes axe with pictures and switches', async () => {
    fakeFetch({ 'GET /api/actions': { status: 200, body: offers() } });
    mountCams('large');
    await flushPromises();
    await nextTick();
    expect(await axeViolations()).toEqual([]);
  });
});

describe('Widget settings for Cameras', () => {
  it('offers the cameras’ own settings, and stores only what differs from the default', async () => {
    const { default: TileEditor } = await import('../src/board/TileEditor.vue');
    const hs = harness(house());
    const ed = hs.mountIn(TileEditor, { item: { id: 'cameras@app:frigate', type: 'cameras' }, config: { name: 'Outside' } }).findComponent(TileEditor);
    await flushPromises();
    const picks = ed.findAll('fieldset.picks input[type="checkbox"]');
    expect(ed.find('fieldset.picks legend').text()).toBe('Cameras');
    expect(picks).toHaveLength(4);
    // Saved as is: every option at its default.
    await ed.find('form').trigger('submit');
    expect(ed.emitted('save')![0]![0]).toEqual({ name: 'Outside' });

    await picks[0]!.setValue(true); // Driveway, first by name
    const boxes = ed.findAll('.field.check input[type="checkbox"]');
    await boxes[0]!.setValue(false); // Show pictures
    const refresh = ed.findAll('select').find((s) => s.findAll('option').some((o) => o.text() === '2 seconds'))!;
    await refresh.setValue('30');
    await ed.find('form').trigger('submit');
    expect(ed.emitted('save')![1]![0]).toEqual({ name: 'Outside', options: { cameras: ['frigate:camera/driveway'], pictures: false, refresh: '30' } });
  });
});

describe('a camera’s sheet', () => {
  it('shows the current picture and the camera’s switches as facts', async () => {
    fakeFetch({ 'GET /api/actions': { status: 200, body: offers() }, 'GET /api/history/states': { status: 200, body: [] } });
    const { default: ResourceSheet } = await import('../src/components/ResourceSheet.vue');
    const hs = harness(house());
    const w = hs.mountIn(ResourceSheet);
    hs.ui.openSheet('frigate:camera/front_door');
    await flushPromises();
    await nextTick();
    const img = document.querySelector('.rs-pic img');
    expect(img?.getAttribute('src')).toContain('resource=frigate%3Acamera%2Ffront_door');
    const text = document.querySelector('.rs-facts')?.textContent ?? '';
    expect(text).toContain('DetectionOn · 5 fps');
    expect(text).toContain('AlertsOff');
    expect(text).toContain('Last personperson'.slice(0, 11));
    expect(document.querySelector('[role="tab"]:nth-child(2)')?.textContent).toContain('Actions');
    w.unmount();
  });
});

describe('camera order and sizes', () => {
  const cells = (w: ReturnType<typeof mountCams>['w']) =>
    w.findAll('.cams .cam').map((c) => `${c.find('.cam-ts').text().split(' ')[0]} ${c.attributes('style')?.match(/grid-column: ([^;]+);/)?.[1]} | ${c.attributes('style')?.match(/grid-row: ([^;]+);/)?.[1]}`);

  it('packs the cameras in the order picked, the first big on Auto', async () => {
    fakeFetch({ 'GET /api/actions': { status: 200, body: [] } });
    const { w } = mountCams('large', { options: { cameras: ['frigate:camera/garage', 'frigate:camera/garden', 'frigate:camera/driveway'] } });
    await flushPromises();
    expect(cells(w)).toEqual(['GARAGE 1 / span 2 | 1 / span 1', 'GARDEN 1 / span 1 | 2 / span 1', 'DRIVEWAY 2 / span 1 | 2 / span 1']);
  });

  it('gives each camera the room set for it, and counts those that don’t fit', async () => {
    fakeFetch({ 'GET /api/actions': { status: 200, body: [] } });
    const { w } = mountCams('hero', {
      options: { camera_sizes: { 'frigate:camera/driveway': '1x1', 'frigate:camera/front_door': '2x2', 'frigate:camera/garden': '1x2', 'frigate:camera/garage': '2x2' } },
    });
    await flushPromises();
    // 4 × 2: driveway one cell, the front door 2 × 2, the garden 2 tall; no room left for the garage.
    expect(cells(w)).toEqual(['DRIVEWAY 1 / span 1 | 1 / span 1', 'FRONT 2 / span 2 | 1 / span 2', 'GARDEN 4 / span 1 | 1 / span 2']);
    expect(w.find('.tf').text()).toContain('4 cameras (+1 more)');
  });

  it('clamps a size to the tile, and ignores a malformed one', async () => {
    fakeFetch({ 'GET /api/actions': { status: 200, body: [] } });
    const { w } = mountCams('wide', { options: { camera_sizes: { 'frigate:camera/driveway': '2x2', 'frigate:camera/front_door': 'huge' } } });
    await flushPromises();
    expect(cells(w)).toEqual(['DRIVEWAY 1 / span 2 | 1 / span 1']);
    expect(w.find('.tf').text()).toContain('(+3 more)');
  });

  it('reorders and sizes cameras in Widget settings', async () => {
    const { default: TileEditor } = await import('../src/board/TileEditor.vue');
    const hs = harness(house());
    const ed = hs.mountIn(TileEditor, { item: { id: 'cameras@app:frigate', type: 'cameras' }, config: { options: { cameras: ['frigate:camera/garden', 'frigate:camera/driveway'] } } }).findComponent(TileEditor);
    await flushPromises();
    const names = () => ed.findAll('fieldset.picks .pick-name').map((x) => x.text());
    // Ticked first, in their order; then the rest by name.
    expect(names()).toEqual(['Garden', 'Driveway', 'Front door', 'Garage']);
    expect(ed.find('[aria-label="Move Garden earlier"]').attributes('disabled')).toBeDefined();
    expect(ed.find('[aria-label="Move Garage later"]').exists()).toBe(false);
    await ed.find('[aria-label="Move Driveway earlier"]').trigger('click');
    expect(names()).toEqual(['Driveway', 'Garden', 'Front door', 'Garage']);
    expect(await axeViolations()).toEqual([]);
    await ed.find('[aria-label="Size of Garden"]').setValue('2x2');
    await ed.find('[aria-label="Size of Front door"]').setValue('1x2');
    await ed.find('[aria-label="Size of Front door"]').setValue('auto');
    await ed.find('form').trigger('submit');
    expect(ed.emitted('save')![0]![0]).toEqual({
      options: { cameras: ['frigate:camera/driveway', 'frigate:camera/garden'], camera_sizes: { 'frigate:camera/garden': '2x2' } },
    });
  });
});
