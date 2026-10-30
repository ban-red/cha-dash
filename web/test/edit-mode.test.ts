import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, type VueWrapper } from '@vue/test-utils';
import { defineComponent, h, reactive, shallowRef } from 'vue';
import { LayoutBoard } from '@chadash/layout-vue';
import { remove, resize, type Board } from '@chadash/layout';
import BoardSurface from '../src/board/BoardSurface.vue';
import TileEditor from '../src/board/TileEditor.vue';
import WidgetPicker from '../src/board/WidgetPicker.vue';
import { defaultBoard } from '../src/model/defaultLayout';
import { WIDGET_GROUPS, WIDGETS } from '../src/model/widgets';
import { setDisplayTimeZone } from '../src/lib/format';
import { navigate, route } from '../src/lib/router';
import { applyTheme } from '../src/lib/theme';
import { tileInstance } from '../src/model/integrations';
import { provideBoard, type BoardStore } from '../src/stores/board';
import type { TilesDoc } from '../src/stores/tiles';
import { docs, withIncidents } from './fixtures';
import { fakeFetch } from './fake-fetch';
import { harness } from './harness';

beforeEach(() => {
  setDisplayTimeZone('UTC');
  applyTheme('instrument-dark');
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

/** A board store that keeps the saved board and tiles, like the real one after a 200. */
function fakeStore(initial: Board, save: (b: Board) => Promise<boolean> = async () => true, tiles: TilesDoc = {}) {
  const board = shallowRef<Board | undefined>(initial);
  const t = shallowRef<TilesDoc>(tiles);
  const store = {
    state: reactive({ unsaved: false, boardId: 'b1', status: 'ready' }),
    board,
    tiles: t,
    load: vi.fn(),
    save: vi.fn(async (b: Board, nt?: TilesDoc) => {
      board.value = b;
      if (nt !== undefined) t.value = nt;
      return save(b);
    }),
  };
  return store as unknown as BoardStore & { save: typeof store.save };
}

function mountSurface(store: BoardStore) {
  const hs = harness(docs(withIncidents));
  const Page = defineComponent({
    setup() {
      provideBoard(store);
      return () => h(BoardSurface, { board: store.board.value!, breakpoint: 'desktop' });
    },
  });
  return Object.assign(hs.mountIn(Page), { ui: hs.ui, discovery: hs.discovery, session: hs.session });
}

/** Edit mode, the picker and the editor load on demand: wait for the import, then for Vue. */
async function settle() {
  await vi.dynamicImportSettled();
  await flushPromises();
}

const start = () => defaultBoard(docs(withIncidents).graph, Date.parse(withIncidents.now));
const button = (w: VueWrapper, text: string) => w.findAll('button').find((b) => b.text() === text)!;
const ids = (b: Board) => b.sections.flatMap((s) => s.items.map((i) => i.id));

async function editing(store: BoardStore) {
  const w = mountSurface(store);
  w.ui.state.editing = true;
  await settle();
  return w;
}

/** Select a tile the way focusing it does. */
async function select(w: VueWrapper, id: string) {
  w.findComponent(LayoutBoard).vm.$emit('select', id);
  await flushPromises();
}

describe('edit mode', () => {
  it('swaps in the layout renderer on Edit and back on Done', async () => {
    const w = mountSurface(fakeStore(start()));
    expect(w.findComponent(LayoutBoard).exists()).toBe(false);

    w.ui.state.editing = true;
    await settle();
    expect(w.findComponent(LayoutBoard).exists()).toBe(true);
    expect(w.findComponent(LayoutBoard).props('editing')).toBe(true);
    expect(w.findAll('.tile')).toHaveLength(17); // the same widgets, now editable
    const undo = w.findAll('.edit-bar button').find((b) => b.text() === 'Undo')!;
    expect(undo.attributes('disabled')).toBeDefined();

    await w.findAll('.edit-bar button').find((b) => b.text() === 'Done')!.trigger('click');
    await flushPromises();
    expect(w.findComponent(LayoutBoard).exists()).toBe(false);
    expect(w.ui.state.editing).toBe(false);
  });

  it('saves each committed gesture, and undo saves the previous board', async () => {
    const before = start();
    const store = fakeStore(before);
    const w = await editing(store);

    const item = before.sections.flatMap((s) => s.items).find((i) => i.supportedSizes.length > 1 && i.size !== i.supportedSizes[0])!;
    const after = resize(before, item.id, item.supportedSizes[0]!);
    w.findComponent(LayoutBoard).vm.$emit('commit', after);
    await flushPromises();
    expect(store.save).toHaveBeenLastCalledWith(after); // a drag leaves the tiles alone

    const undo = w.findAll('.edit-bar button').find((b) => b.text() === 'Undo')!;
    expect(undo.attributes('disabled')).toBeUndefined();
    await undo.trigger('click');
    await flushPromises();
    expect(store.save).toHaveBeenLastCalledWith(before);
  });

  it('tells you when someone else saved first', async () => {
    const before = start();
    const w = await editing(fakeStore(before, async () => false));

    const item = before.sections.flatMap((s) => s.items).find((i) => i.supportedSizes.length > 1 && i.size !== i.supportedSizes[0])!;
    w.findComponent(LayoutBoard).vm.$emit('commit', resize(before, item.id, item.supportedSizes[0]!));
    await flushPromises();
    expect(w.ui.state.toasts.map((t) => t.text).join()).toContain('This board changed somewhere else');
  });
});

describe('removing a tile', () => {
  it('removes the selected tile and its config; undo brings both back', async () => {
    const before = start();
    const tiles: TilesDoc = { plex: { name: 'Living room', href: 'https://plex.home.example' } };
    const store = fakeStore(before, async () => true, tiles);
    const w = await editing(store);

    expect(w.find('.edit-sel').exists()).toBe(false);
    await select(w, 'plex');
    expect(w.find('.edit-sel-name').text()).toBe('Now playing: Living room');
    await button(w, 'Remove').trigger('click');
    await flushPromises();
    const [board, saved] = store.save.mock.lastCall!;
    expect(board).toEqual(remove(before, 'plex'));
    expect(saved).toEqual({});
    expect(w.findAll('.tile')).toHaveLength(16);
    expect(w.ui.state.toasts.at(-1)!.text).toBe('Removed Now playing: Living room. Undo puts it back.');
    // focus (and so the selection) moves to its neighbour in the section
    const media = before.sections.find((s) => s.items.some((i) => i.id === 'plex'))!.items;
    const neighbour = media[media.findIndex((i) => i.id === 'plex') + 1]!.id;
    expect((document.activeElement as HTMLElement | null)?.dataset['itemId']).toBe(neighbour);

    await button(w, 'Undo').trigger('click');
    await flushPromises();
    expect(store.save).toHaveBeenLastCalledWith(before, tiles);
    expect(w.findAll('.tile')).toHaveLength(17);
  });

  it('removes the focused tile with Delete, but not one that is picked up', async () => {
    const before = start();
    const store = fakeStore(before);
    const w = await editing(store);
    const el = () => w.find('[data-item-id="incidents"]');
    await el().trigger('keydown', { key: 'Enter' }); // picked up
    await el().trigger('keydown', { key: 'Delete' });
    expect(store.save).not.toHaveBeenCalled();
    await el().trigger('keydown', { key: 'Escape' }); // put back
    await el().trigger('keydown', { key: 'Delete' });
    await flushPromises();
    expect(ids(store.save.mock.lastCall![0])).not.toContain('incidents');
  });

  it('remembers a removed tile found by its labels, so it isn’t suggested again', async () => {
    const f = fakeFetch({ 'POST /api/boards/b1/discovery/dismiss': { status: 200, body: { dismissed: ['docker:i_d/container/mealie'] } } });
    const before = start();
    const w = await (async () => {
      const store = fakeStore(before, async () => true, {
        plex: { name: 'Mealie', bind: { resource: 'docker:i_d/container/mealie' }, origin: { source: 'labels', group: 'Media', entry: 'Mealie' } },
      });
      const hs = harness(docs(withIncidents));
      hs.board.state.boardId = 'b1';
      const Page = defineComponent({
        setup() {
          provideBoard(store);
          return () => h(BoardSurface, { board: store.board.value!, breakpoint: 'desktop' });
        },
      });
      const wrapper = hs.mountIn(Page);
      hs.ui.state.editing = true;
      await settle();
      return wrapper;
    })();
    await select(w, 'plex');
    await button(w, 'Remove').trigger('click');
    await flushPromises();
    expect(f.last('POST', '/api/boards/b1/discovery/dismiss')!.body).toEqual({ resources: ['docker:i_d/container/mealie'] });
  });
});

describe('editing a tile', () => {
  it('renames and relinks the selected tile through the history', async () => {
    const before = start();
    const store = fakeStore(before);
    const w = await editing(store);
    await select(w, 'plex');
    await button(w, 'Edit').trigger('click');
    await settle();
    const ed = w.findComponent(TileEditor);
    expect(ed.exists()).toBe(true);
    expect(document.querySelector('dialog.editor')?.hasAttribute('open')).toBe(true);
    await ed.find('input[type="text"]').setValue('Living room');
    await ed.find('input[type="url"]').setValue('ftp://nope');
    expect(ed.find('.help.bad').text()).toContain('http:// or https://');
    expect(ed.find('button[type="submit"]').attributes('disabled')).toBeDefined();
    await ed.find('input[type="url"]').setValue('https://plex.home.example');
    await ed.find('form').trigger('submit');
    await flushPromises();

    expect(store.save).toHaveBeenLastCalledWith(before, { plex: { name: 'Living room', href: 'https://plex.home.example' } });
    expect(w.findComponent(TileEditor).exists()).toBe(false);
    // Tile.vue shows the tile's own name and link, for any widget
    const tile = w.find('[data-item-id="plex"] .tile');
    expect(tile.find('.tn').text()).toBe('Living room');
    expect(tile.find('a.mono').attributes('href')).toBe('https://plex.home.example');

    await button(w, 'Undo').trigger('click');
    await flushPromises();
    expect(store.save).toHaveBeenLastCalledWith(before, {});
    expect(w.find('[data-item-id="plex"] .tile .tn').text()).not.toBe('Living room');
  });

  it('sets the calendar’s unmonitored option, replacing the importer’s per-source one', async () => {
    const hs = harness(docs(withIncidents));
    const config = {
      name: 'Up next',
      options: { view: 'agenda' },
      bind: { sources: [{ instance: 'i_s', capability: 'media.calendar', options: { unmonitored: true, color: 'teal' } }, { instance: 'i_r', capability: 'media.calendar' }] },
    };
    const ed = hs.mountIn(TileEditor, { item: { id: 'calendar', type: 'calendar' }, config }).findComponent(TileEditor);
    await flushPromises();
    const box = ed.find('input[type="checkbox"]');
    expect((box.element as HTMLInputElement).checked).toBe(true); // a source had it on
    await ed.find('form').trigger('submit');
    expect(ed.emitted('save')![0]![0]).toEqual({
      name: 'Up next',
      options: { view: 'agenda', unmonitored: true },
      bind: { sources: [{ instance: 'i_s', capability: 'media.calendar', options: { color: 'teal' } }, { instance: 'i_r', capability: 'media.calendar' }] },
    });
    await box.setValue(false);
    await ed.find('form').trigger('submit');
    expect(ed.emitted('save')![1]![0]).toMatchObject({ options: { view: 'agenda' } });
    expect((ed.emitted('save')![1]![0] as { options: object }).options).not.toHaveProperty('unmonitored');
    // Other widgets don't get the box.
    const other = hs.mountIn(TileEditor, { item: { id: 'plex', type: 'plex' } });
    expect(other.find('input[type="checkbox"]').exists()).toBe(false);
  });

  it('points a bound widget at another resource without changing its id', async () => {
    const before = start();
    const store = fakeStore(before);
    const w = await editing(store);
    const arr = ids(before).find((id) => id.startsWith('arr@'))!;
    await select(w, arr);
    await button(w, 'Edit').trigger('click');
    await settle();
    const ed = w.findComponent(TileEditor);
    const options = ed.findAll('select option').map((o) => o.attributes('value'));
    expect(options).toEqual(expect.arrayContaining(['app:sonarr', 'app:radarr']));
    const other = arr.endsWith('sonarr') ? 'app:radarr' : 'app:sonarr';
    await ed.find('select').setValue(other);
    await ed.find('form').trigger('submit');
    await flushPromises();
    expect(store.save.mock.lastCall![1]).toEqual({ [arr]: { bind: { resource: other } } });
    expect(ids(store.save.mock.lastCall![0])).toContain(arr);
  });
});

describe('label discovery in edit mode', () => {
  /** Edit mode, with the harness's discovery reading board b1 (the fake store draws it). */
  async function editingDiscovered(store: BoardStore) {
    const hs = harness(docs(withIncidents));
    hs.board.state.boardId = 'b1';
    hs.board.state.status = 'ready';
    const Page = defineComponent({
      setup() {
        provideBoard(store);
        return () => h(BoardSurface, { board: store.board.value!, breakpoint: 'desktop' });
      },
    });
    const w = Object.assign(hs.mountIn(Page), { ui: hs.ui });
    w.ui.state.editing = true;
    await settle();
    await settle();
    return w;
  }

  it('lists dismissed services and offers one again', async () => {
    const restored: unknown[] = [];
    let dismissed = ['docker:magpie/mealie', 'docker:magpie/pbs'];
    fakeFetch({
      'GET /api/boards/b1/discovery': () => ({ status: 200, body: { dismissed } }),
      'POST /api/boards/b1/discovery/restore': (body) => {
        restored.push(body);
        const gone = new Set((body as { resources: string[] }).resources);
        dismissed = dismissed.filter((r) => !gone.has(r));
        return { status: 200, body: { dismissed } };
      },
    });
    const w = await editingDiscovered(fakeStore(start()));
    const open = w.findAll('.edit-bar button').find((b) => b.text().startsWith('Dismissed'))!;
    expect(open.text()).toBe('Dismissed · 2');
    await open.trigger('click');
    await settle();
    const rows = [...document.querySelectorAll('dialog.dismissed li')];
    expect(rows.map((li) => li.querySelector('.ds-name')!.textContent)).toEqual(['Mealie', 'Proxmox Backup Server']);
    rows[0]!.querySelector('button')!.click();
    await flushPromises();
    expect(restored).toEqual([{ resources: ['docker:magpie/mealie'] }]);
    expect(w.ui.state.toasts.at(-1)!.text).toBe('Mealie will be offered again in the Inbox.');
    expect([...document.querySelectorAll('dialog.dismissed li')]).toHaveLength(1);
    expect(w.findAll('.edit-bar button').find((b) => b.text().startsWith('Dismissed'))!.text()).toBe('Dismissed · 1');
  });

  it('has no Dismissed button when nothing is dismissed', async () => {
    fakeFetch({ 'GET /api/boards/b1/discovery': { status: 200, body: { dismissed: [] } } });
    const w = await editingDiscovered(fakeStore(start()));
    expect(w.findAll('.edit-bar button').some((b) => b.text().startsWith('Dismissed'))).toBe(false);
  });
});

describe('adding a widget', () => {
  it('opens the picker when ⌘K asked for it', async () => {
    const w = mountSurface(fakeStore(start()));
    w.ui.state.editing = true;
    w.ui.state.addingWidget = true;
    await settle();
    await settle();
    expect(w.findComponent(WidgetPicker).exists()).toBe(true);
    expect(w.ui.state.addingWidget).toBe(false);
    expect(document.querySelector('dialog.picker')?.hasAttribute('open')).toBe(true);
  });

  it('adds from a section header without edit mode, which starts once the widget is picked', async () => {
    const before = start();
    const store = fakeStore(remove(before, 'cameras'));
    const w = mountSurface(store);
    await settle();
    const btns = w.findAll('.sh-live .sh-add');
    expect(btns).toHaveLength(before.sections.length);
    const at = before.sections.findIndex((s) => s.id === 'home');
    await btns[at]!.trigger('click');
    await settle();
    await settle();
    // The picker is open on that section, over the board as it is
    expect(w.ui.state.editing).toBe(false);
    const pk = w.findComponent(WidgetPicker);
    expect(pk.exists()).toBe(true);
    expect((pk.find('.pk-to select').element as HTMLSelectElement).value).toBe('home');
    expect(w.ui.state.addSection).toBe('');
    expect(w.ui.state.addingWidget).toBe(false);

    await pk.find('button.pk-item[data-type="cameras"]').trigger('click');
    await pk.find('form').trigger('submit');
    await flushPromises();
    await settle();
    await settle();
    // Now it's edit mode, with the widget added as a step of it
    expect(w.ui.state.editing).toBe(true);
    expect(w.ui.state.pendingAdd).toBeUndefined();
    const [board] = store.save.mock.lastCall!;
    expect(board.sections.find((s) => s.id === 'home')!.items.map((i) => i.id)).toContain('cameras');
    expect(w.ui.state.toasts.at(-1)!.text).toBe('Added Cameras to Home.');
  });

  it('closing the picker from a header leaves edit mode off', async () => {
    const w = mountSurface(fakeStore(start()));
    await settle();
    await w.find('.sh-live .sh-add').trigger('click');
    await settle();
    await settle();
    const pk = w.findComponent(WidgetPicker);
    expect(pk.exists()).toBe(true);
    pk.vm.$emit('close');
    await settle();
    expect(w.findComponent(WidgetPicker).exists()).toBe(false);
    expect(w.ui.state.editing).toBe(false);
  });

  it('loads the picker on demand and lists the catalog by what the graph can feed', async () => {
    const before = start();
    const store = fakeStore(remove(before, 'cameras'));
    const w = await editing(store);
    expect(w.findComponent(WidgetPicker).exists()).toBe(false);
    await w.find('.sh-add').trigger('click'); // the first section's Add
    await settle();
    const pk = w.findComponent(WidgetPicker);
    expect(pk.exists()).toBe(true);
    expect((pk.find('.pk-to select').element as HTMLSelectElement).value).toBe(before.sections[0]!.id);

    const readyTypes = pk.findAll('button.pk-item').map((b) => b.attributes('data-type'));
    const waiting = pk.findAll('.pk-waiting .pk-item').map((b) => [b.attributes('data-type'), b.find('.pk-sub').text()]);
    // every catalog entry is listed once, read generically
    expect([...readyTypes, ...waiting.map(([t]) => t)].sort()).toEqual(Object.keys(WIDGETS).sort());
    expect(readyTypes).toContain('cameras'); // removed, the graph has cameras
    expect(readyTypes).toContain('service');
    expect(waiting).toContainEqual(['plex', 'Already on this board']);
    expect(waiting).toContainEqual(['arr', 'Every one is on this board already']);
  });

  it('places the chosen widget in the section, in a size that fits, and undo takes it out', async () => {
    const before = start();
    const board0 = remove(before, 'cameras');
    const store = fakeStore(board0);
    const w = await editing(store);
    await button(w, 'Add widget').trigger('click');
    await settle();
    const pk = w.findComponent(WidgetPicker);
    await pk.find('button.pk-item[data-type="cameras"]').trigger('click');
    const sizes = pk.findAll('.pk-size input').map((i) => (i.element as HTMLInputElement).value);
    expect(sizes).toEqual(WIDGETS['cameras']!.sizes.filter((s) => s !== 'hero')); // Home is 4 wide: no 8×4
    await pk.find('form').trigger('submit');
    await flushPromises();

    const [board, tiles] = store.save.mock.lastCall!;
    expect(tiles).toBeUndefined(); // a catalog widget needs no config
    const home = board.sections.find((s) => s.id === 'home')!;
    expect(home.items.map((i) => i.id)).toContain('cameras');
    expect(home.items.find((i) => i.id === 'cameras')!.size).toBe('large');
    expect(w.findComponent(WidgetPicker).exists()).toBe(false);
    expect(w.ui.state.toasts.at(-1)!.text).toBe('Added Cameras to Home.');

    await button(w, 'Undo').trigger('click');
    await flushPromises();
    expect(store.save).toHaveBeenLastCalledWith(board0);
  });

  it('binds a widget that needs one resource to the one you pick', async () => {
    const before = start();
    const arr = ids(before).find((id) => id.startsWith('arr@'))!;
    const store = fakeStore(remove(before, arr));
    const w = await editing(store);
    await button(w, 'Add widget').trigger('click');
    await settle();
    const pk = w.findComponent(WidgetPicker);
    await pk.find('.pk-to select').setValue('media');
    await pk.find('button.pk-item[data-type="arr"]').trigger('click');
    const choices = pk.findAll<HTMLInputElement>('.pk-target input');
    const free = choices.filter((i) => !i.element.disabled).map((i) => i.element.value);
    expect(free).toEqual([arr.slice('arr@'.length)]);
    // the other one is still on the board: listed, but it can't be picked twice
    expect(choices.filter((i) => i.element.disabled)).toHaveLength(1);
    expect(pk.find('.pk-target.taken').text()).toContain('On the board');
    expect(choices.find((i) => !i.element.disabled)!.element.checked).toBe(true);
    await pk.find('form').trigger('submit');
    await flushPromises();
    expect(ids(store.save.mock.lastCall![0])).toContain(arr);
  });

  it('adds a service tile with its name, link and status binding', async () => {
    const store = fakeStore(start());
    const w = await editing(store);
    await button(w, 'Add widget').trigger('click');
    await settle();
    const pk = w.findComponent(WidgetPicker);
    await pk.find('button.pk-item[data-type="service"]').trigger('click');
    expect(pk.find('button[type="submit"]').attributes('aria-disabled')).toBe('true'); // a name first
    expect(pk.find('.pk-foot-bar .sr').text()).toBe('Give it a name first.');
    await pk.find('input[type="text"]').setValue('Router admin');
    await pk.find('input[type="url"]').setValue('https://router.home.example');
    const bind = pk.findAll('.pk-detail select')[0]!;
    expect(bind.findAll('optgroup').map((g) => g.attributes('label'))).toEqual(expect.arrayContaining(['Integrations', 'Services and containers']));
    await bind.setValue('instance:opnsense');
    await pk.find('form').trigger('submit');
    await flushPromises();

    const [board, tiles] = store.save.mock.lastCall!;
    const id = ids(board).find((i) => i.startsWith('service@tile:'))!;
    expect(id).toBeDefined();
    expect(tiles).toEqual({ [id]: { name: 'Router admin', href: 'https://router.home.example', bind: { instance: 'opnsense' } } });
    expect(w.find(`[data-item-id="${id}"] .tn`).text()).toBe('Router admin');
  });
});

describe('the widget picker', () => {
  async function open(board = start()) {
    const store = fakeStore(board);
    const w = await editing(store);
    await button(w, 'Add widget').trigger('click');
    await settle();
    return { w, store, pk: w.findComponent(WidgetPicker) };
  }
  const cardTypes = (pk: Awaited<ReturnType<typeof open>>['pk']) => pk.findAll('button.pk-item').map((b) => b.attributes('data-type'));

  it('opens on the first ready widget, previewed live at its size', async () => {
    const { pk } = await open(remove(start(), 'cameras'));
    const first = pk.find('button.pk-item');
    expect(first.attributes('aria-pressed')).toBe('true');
    const type = first.attributes('data-type')!;
    expect(pk.find('.pk-title').text()).toBe(WIDGETS[type]!.name);
    const stage = pk.find('.pk-preview');
    expect(stage.attributes('inert')).toBeDefined(); // a picture, not a second set of links
    expect(stage.find('.tile, .clock').exists()).toBe(true);
  });

  it('searches the catalog, and Enter in the search picks the top match without adding', async () => {
    const { pk, store } = await open(remove(start(), 'cameras'));
    const saves = store.save.mock.calls.length;
    await pk.find('input[type="search"]').setValue('frigate');
    expect(cardTypes(pk)[0]).toBe('cameras'); // found first by what it reads, not only its name
    await pk.find('input[type="search"]').trigger('keydown', { key: 'Enter' });
    expect(pk.find('button.pk-item[data-type="cameras"]').attributes('aria-pressed')).toBe('true');
    expect(store.save.mock.calls.length).toBe(saves);
    await pk.find('input[type="search"]').setValue('stork');
    expect(cardTypes(pk)).toEqual(expect.arrayContaining(['host', 'metric'])); // and by what it can show

    await pk.find('input[type="search"]').setValue('zzzz');
    expect(cardTypes(pk)).toEqual([]);
    expect(pk.find('.pk-empty').text()).toContain('No widget that fits here matches “zzzz”');
    await pk.findAll('.pk-empty button').find((b) => b.text() === 'Clear the search')!.trigger('click');
    expect(cardTypes(pk).length).toBeGreaterThan(1);
  });

  it('files widgets in groups, with a count on each chip', async () => {
    const { pk } = await open();
    const chips = pk.findAll('.pk-groups .pk-chip');
    expect(chips.map((c) => c.text().replace(/\d+$/, ''))).toEqual(['All', ...WIDGET_GROUPS.map((g) => g.name)]);
    const media = chips.find((c) => c.text().startsWith('Media'))!;
    await media.trigger('click');
    expect(media.attributes('aria-pressed')).toBe('true');
    const shown = cardTypes(pk);
    expect(shown.length).toBe(Number(media.find('.n').text()));
    expect(shown.every((t) => WIDGETS[t!]!.group === 'media')).toBe(true);
  });

  it('resizes the preview with the size picked', async () => {
    const { pk } = await open(remove(start(), 'cameras'));
    await pk.find('button.pk-item[data-type="cameras"]').trigger('click');
    const style = () => pk.find('.pk-preview').attributes('style');
    expect(style()).toContain('--w: 4; --h: 4'); // large, its default
    await pk.find('.pk-size input[value="small"]').setValue(true);
    expect(style()).toContain('--w: 2; --h: 2');
    expect(pk.find('.pk-stage figcaption').text()).toContain('Small 2×2');
  });

  it('moves through the cards with the arrow keys, the preview following, and ⌘↵ adds', async () => {
    const { pk, w, store } = await open(remove(start(), 'cameras'));
    await pk.find('input[type="search"]').trigger('keydown', { key: 'ArrowDown' });
    const types = cardTypes(pk);
    expect(pk.find('button.pk-item[aria-pressed="true"]').attributes('data-type')).toBe(types[0]);
    await pk.find(`button.pk-item[data-type="${types[0]}"]`).trigger('keydown', { key: 'ArrowDown' });
    expect(pk.find('button.pk-item[aria-pressed="true"]').attributes('data-type')).toBe(types[1]);
    expect(pk.find('.pk-title').text()).toBe(WIDGETS[types[1]!]!.name);

    await pk.find('button.pk-item[data-type="cameras"]').trigger('click');
    await pk.find('dialog').trigger('keydown', { key: 'Enter', metaKey: true });
    await flushPromises();
    expect(ids(store.save.mock.lastCall![0])).toContain('cameras');
    expect(w.findComponent(WidgetPicker).exists()).toBe(false);
  });

  it('adds on a double-click', async () => {
    const { pk, store } = await open(remove(start(), 'cameras'));
    await pk.find('button.pk-item[data-type="cameras"]').trigger('dblclick');
    await flushPromises();
    await settle();
    expect(ids(store.save.mock.lastCall![0])).toContain('cameras');
  });

  it('filters a long list of what a widget can show', async () => {
    const { pk } = await open();
    await pk.find('button.pk-item[data-type="metric"]').trigger('click');
    const rows = () => pk.findAll('.pk-target:not(.taken) input').map((i) => (i.element as HTMLInputElement).value);
    const all = rows();
    expect(all.length).toBeGreaterThan(6);
    const q = pk.find('.pk-target-q');
    expect(q.exists()).toBe(true);
    await q.setValue('cpu');
    expect(rows().length).toBeGreaterThan(0);
    expect(rows().length).toBeLessThan(all.length);
    expect(rows().every((v) => v.includes('cpu'))).toBe(true);
  });
});

describe('a tile’s menu', () => {
  afterEach(() => navigate('/', { replace: true }));
  const items = () => [...document.querySelectorAll<HTMLElement>('.tile-pop [role^="menuitem"]')];
  const item = (text: string) => items().find((e) => e.textContent?.trim().startsWith(text))!;

  it('opens the settings of the integration that feeds the tile', async () => {
    const w = mountSurface(fakeStore(start()));
    await settle();
    const cog = w.find('[data-item="plex"] .tile-cog');
    expect(cog.attributes('aria-label')).toMatch(/ menu$/);
    await cog.trigger('click');
    await settle();
    expect(items().map((e) => /^(Integration settings|Widget settings|Resize|Reorder|Remove)/.exec(e.textContent?.trim() ?? '')?.[1])).toEqual(['Integration settings', 'Widget settings', 'Resize', 'Reorder', 'Remove']);
    item('Integration settings').click();
    await settle();
    expect(route.value.path).toBe('/settings/integrations/plex');
    expect(w.ui.state.editing).toBe(false);
  });

  it('opens the tile editor over the normal board, without edit mode', async () => {
    const store = fakeStore(start());
    const w = mountSurface(store);
    await settle();
    await w.find('[data-item="clock"] .tile-cog').trigger('click');
    await settle();
    expect(items().some((e) => e.textContent?.includes('Integration settings'))).toBe(false);
    item('Widget settings').click();
    await settle();
    expect(w.ui.state.editing).toBe(false);
    expect(w.findComponent(TileEditor).props('item')).toMatchObject({ id: 'clock' });
    expect(w.find('.tile-cog').exists()).toBe(true);
  });

  it('resizes the tile from the menu and saves the layout', async () => {
    const store = fakeStore(start());
    const w = mountSurface(store);
    await settle();
    await w.find('[data-item="clock"] .tile-cog').trigger('click');
    await settle();
    item('Resize').click();
    await settle();
    const before = [...document.querySelectorAll('.tile-pop [aria-checked="true"]')].length;
    expect(before).toBe(1);
    const other = items().find((e) => e.getAttribute('aria-checked') === 'false')!;
    other.click();
    await settle();
    expect(store.save).toHaveBeenCalled();
    expect(document.querySelector('.tile-pop')).toBeNull();
  });

  it('reorders the tile within its section and saves the layout', async () => {
    const store = fakeStore(start());
    const w = mountSurface(store);
    await settle();
    const section = store.board.value!.sections.find((s) => s.items.some((i) => i.id === 'clock'))!;
    const was = section.items.findIndex((i) => i.id === 'clock');
    await w.find('[data-item="clock"] .tile-cog').trigger('click');
    await settle();
    item('Reorder').click();
    await settle();
    const names = items().map((e) => e.textContent?.trim());
    expect(names.length).toBeGreaterThan(0);
    const first = names.includes('Move earlier') ? 'Move earlier' : 'Move later';
    item(first).click();
    await settle();
    const now = store.board.value!.sections.find((s) => s.id === section.id)!.items.findIndex((i) => i.id === 'clock');
    expect(now).toBe(first === 'Move earlier' ? was - 1 : was + 1);
    expect(store.save).toHaveBeenCalled();
  });

  it('asks before removing, and Undo puts the tile back', async () => {
    const store = fakeStore(start());
    const w = mountSurface(store);
    await settle();
    await w.find('[data-item="clock"] .tile-cog').trigger('click');
    await settle();
    item('Remove').click();
    await settle();
    expect(document.querySelector('.tile-pop [role="alertdialog"]')).not.toBeNull();
    expect(store.save).not.toHaveBeenCalled();
    document.querySelector<HTMLElement>('.tile-pop [data-confirm]')!.click(); // Cancel
    await settle();
    expect(store.save).not.toHaveBeenCalled();
    item('Remove').click();
    await settle();
    [...document.querySelectorAll<HTMLElement>('.tile-pop [data-confirm]')].find((b) => b.textContent === 'Remove')!.click();
    await settle();
    expect(store.save).toHaveBeenCalledTimes(1);
    expect(w.ui.state.toasts.at(-1)?.action?.label).toBe('Undo');
  });

  it('isn’t there for a role that can do none of it', async () => {
    const w = mountSurface(fakeStore(start()));
    await settle();
    expect(w.find('.tile-cog').exists()).toBe(true);
    w.session.state.user!.role = 'viewer';
    await flushPromises();
    expect(w.find('.tile-cog').exists()).toBe(false);
  });
});

describe('leaving edit mode', () => {
  it('has an exit button and Escape leaves edit mode', async () => {
    const w = mountSurface(fakeStore(start()));
    await settle();
    w.ui.state.editing = true;
    await settle();
    expect(w.find('.edit-exit').exists()).toBe(true);
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await settle();
    expect(w.ui.state.editing).toBe(false);
    w.ui.state.editing = true;
    await settle();
    await w.find('.edit-exit').trigger('click');
    await settle();
    expect(w.ui.state.editing).toBe(false);
  }, 15_000); // the first to load edit mode's lazy parts: on a busy CI runner that passed 5 s
});

describe('a section header’s menu', () => {
  it('resizes the section from its Resize item and saves the layout', async () => {
    const store = fakeStore(start());
    const w = mountSurface(store);
    await settle();
    const btn = w.find('.sh-live .sh-menu');
    expect(btn.attributes('aria-label')).toMatch(/section menu$/);
    await btn.trigger('click');
    await settle();
    const resize = [...document.querySelectorAll<HTMLElement>('.tile-pop [role="menuitem"]')].find((e) => e.textContent?.includes('Resize'))!;
    resize.click();
    await settle();
    const radios = [...document.querySelectorAll<HTMLElement>('.tile-pop [role="menuitemradio"]')];
    expect(radios.length).toBeGreaterThan(1);
    expect(radios.filter((r) => r.getAttribute('aria-checked') === 'true')).toHaveLength(1);
    radios.find((r) => r.getAttribute('aria-checked') === 'false')!.click();
    await settle();
    expect(store.save).toHaveBeenCalled();
    expect(document.querySelector('.tile-pop')).toBeNull();
  });

  it('moves the section from its Reorder item and saves the layout', async () => {
    const store = fakeStore(start());
    const w = mountSurface(store);
    await settle();
    const before = store.board.value!.sections.map((s) => s.id);
    const header = w.findAll('.sh-live')[1]!; // a middle section can move both ways
    await header.find('.sh-menu').trigger('click');
    await settle();
    [...document.querySelectorAll<HTMLElement>('.tile-pop [role="menuitem"]')].find((e) => e.textContent?.includes('Reorder'))!.click();
    await settle();
    const moves = [...document.querySelectorAll<HTMLElement>('.tile-pop [role="menuitem"]')];
    expect(moves.map((e) => e.textContent?.trim())).toEqual(['Move to top', 'Move up', 'Move down', 'Move to bottom']);
    moves.find((e) => e.textContent?.trim() === 'Move up')!.click();
    await settle();
    const after = store.board.value!.sections.map((s) => s.id);
    expect(after).not.toEqual(before);
    expect([...after].sort()).toEqual([...before].sort());
  });

  it('renames the section from its Section settings item', async () => {
    const store = fakeStore(start());
    const w = mountSurface(store);
    await settle();
    await w.findAll('.sh-live')[1]!.find('.sh-menu').trigger('click');
    await settle();
    [...document.querySelectorAll<HTMLElement>('.tile-pop [role="menuitem"]')].find((e) => e.textContent?.includes('Section settings'))!.click();
    await settle();
    const input = document.querySelector<HTMLInputElement>('.tile-pop input[data-focus]')!;
    input.value = '  Media  ';
    input.dispatchEvent(new Event('input'));
    document.querySelector<HTMLFormElement>('.tile-pop form')!.dispatchEvent(new Event('submit', { cancelable: true }));
    await settle();
    expect(store.board.value!.sections[1]!.title).toBe('Media');
    expect(document.querySelector('.tile-pop')).toBeNull();
  });

  it('adds an empty section before or after from its menu', async () => {
    const store = fakeStore(start());
    const w = mountSurface(store);
    await settle();
    const before = store.board.value!.sections.map((s) => s.id);
    for (const [label, at] of [['before', 1], ['after', 2]] as const) {
      await w.findAll('.sh-live')[1]!.find('.sh-menu').trigger('click');
      await settle();
      [...document.querySelectorAll<HTMLElement>('.tile-pop [role="menuitem"]')].find((e) => e.textContent?.includes(`Add new section ${label}`))!.click();
      await settle();
      const secs = store.board.value!.sections;
      expect(secs[at]!.items).toHaveLength(0);
      expect(secs[at]!.title).toBe('New section');
      expect(secs).toHaveLength(before.length + (label === 'before' ? 1 : 2));
    }
    expect(new Set(store.board.value!.sections.map((s) => s.id)).size).toBe(before.length + 2);
  });

  it('removes the section and its widgets from its Remove section item, after asking', async () => {
    const store = fakeStore(start());
    const w = mountSurface(store);
    await settle();
    const before = store.board.value!.sections.map((s) => s.id);
    const header = w.findAll('.sh-live')[1]!;
    await header.find('.sh-menu').trigger('click');
    await settle();
    [...document.querySelectorAll<HTMLElement>('.tile-pop [role="menuitem"]')].find((e) => e.textContent?.includes('Remove section'))!.click();
    await settle();
    expect(document.querySelector('.tile-pop [role="alertdialog"]')).not.toBeNull();
    expect(store.board.value!.sections).toHaveLength(before.length);
    [...document.querySelectorAll<HTMLElement>('.tile-pop [data-confirm]')].find((e) => e.textContent?.trim() === 'Cancel')!.click();
    await settle();
    expect(document.querySelector('.tile-pop [role="alertdialog"]')).toBeNull();
    expect(store.board.value!.sections).toHaveLength(before.length);
    [...document.querySelectorAll<HTMLElement>('.tile-pop [role="menuitem"]')].find((e) => e.textContent?.includes('Remove section'))!.click();
    await settle();
    [...document.querySelectorAll<HTMLElement>('.tile-pop [data-confirm]')].find((e) => e.textContent?.trim() === 'Remove')!.click();
    await settle();
    const after = store.board.value!.sections.map((s) => s.id);
    expect(after).toHaveLength(before.length - 1);
    expect(after).not.toContain(before[1]);
  });

  it('isn’t there for a role that can’t edit boards', async () => {
    const w = mountSurface(fakeStore(start()));
    await settle();
    w.session.state.user!.role = 'viewer';
    await flushPromises();
    expect(w.find('.sh-menu').exists()).toBe(false);
  });
});

describe('tileInstance', () => {
  const g = docs(withIncidents).graph;
  it('takes the bound instance, a single source, the target’s source or the only app of the type', () => {
    expect(tileInstance(g, 'service', { bind: { instance: 'i_x' } }, 'app:plex')).toBe('i_x');
    expect(tileInstance(g, 'calendar', { bind: { sources: [{ instance: 'i_s', capability: 'c' }, { instance: 'i_s', capability: 'd' }] } }, undefined)).toBe('i_s');
    expect(tileInstance(g, 'arr', undefined, 'app:sonarr')).toBe('sonarr');
    expect(tileInstance(g, 'metric', undefined, 'app:radarr#queue')).toBe('radarr');
    expect(tileInstance(g, 'service', undefined, 'integration:checks')).toBe('checks');
    expect(tileInstance(g, 'plex', undefined, undefined)).toBe('plex');
    expect(tileInstance(g, 'clock', undefined, undefined)).toBeUndefined();
    expect(tileInstance(g, 'service', undefined, 'app:cockpit')).toBeUndefined();
  });
});
