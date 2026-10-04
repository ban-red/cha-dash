import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, type VueWrapper } from '@vue/test-utils';
import { defineComponent, h, reactive, shallowRef } from 'vue';
import { LayoutBoard } from '@chadash/layout-vue';
import { remove, resize, type Board } from '@chadash/layout';
import BoardSurface from '../src/board/BoardSurface.vue';
import TileEditor from '../src/board/TileEditor.vue';
import WidgetPicker from '../src/board/WidgetPicker.vue';
import { defaultBoard } from '../src/model/defaultLayout';
import { WIDGETS } from '../src/model/widgets';
import { setDisplayTimeZone } from '../src/lib/format';
import { applyTheme } from '../src/lib/theme';
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
  return Object.assign(hs.mountIn(Page), { ui: hs.ui, discovery: hs.discovery });
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
  await w.find('.board-tools button').trigger('click');
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

    await w.find('.board-tools button').trigger('click');
    await settle();
    expect(w.findComponent(LayoutBoard).exists()).toBe(true);
    expect(w.findComponent(LayoutBoard).props('editing')).toBe(true);
    expect(w.findAll('.tile')).toHaveLength(15); // the same widgets, now editable
    const undo = w.findAll('.edit-bar button').find((b) => b.text() === 'Undo')!;
    expect(undo.attributes('disabled')).toBeDefined();

    await w.findAll('.edit-bar button').find((b) => b.text() === 'Done')!.trigger('click');
    await flushPromises();
    expect(w.findComponent(LayoutBoard).exists()).toBe(false);
    expect(w.find('.board-tools').exists()).toBe(true);
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
    const tiles: TilesDoc = { plex: { name: 'Living room', href: 'https://plex.lab.example' } };
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
    expect(w.findAll('.tile')).toHaveLength(14);
    expect(w.ui.state.toasts.at(-1)!.text).toBe('Removed Now playing: Living room. Undo puts it back.');
    // focus (and so the selection) moves to its neighbour in the section
    const media = before.sections.find((s) => s.items.some((i) => i.id === 'plex'))!.items;
    const neighbour = media[media.findIndex((i) => i.id === 'plex') + 1]!.id;
    expect((document.activeElement as HTMLElement | null)?.dataset['itemId']).toBe(neighbour);

    await button(w, 'Undo').trigger('click');
    await flushPromises();
    expect(store.save).toHaveBeenLastCalledWith(before, tiles);
    expect(w.findAll('.tile')).toHaveLength(15);
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
      await wrapper.find('.board-tools button').trigger('click');
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
    await ed.find('input[type="url"]').setValue('https://plex.lab.example');
    await ed.find('form').trigger('submit');
    await flushPromises();

    expect(store.save).toHaveBeenLastCalledWith(before, { plex: { name: 'Living room', href: 'https://plex.lab.example' } });
    expect(w.findComponent(TileEditor).exists()).toBe(false);
    // Tile.vue shows the tile's own name and link, for any widget
    const tile = w.find('[data-item-id="plex"] .tile');
    expect(tile.find('.tn').text()).toBe('Living room');
    expect(tile.find('a.mono').attributes('href')).toBe('https://plex.lab.example');

    await button(w, 'Undo').trigger('click');
    await flushPromises();
    expect(store.save).toHaveBeenLastCalledWith(before, {});
    expect(w.find('[data-item-id="plex"] .tile .tn').text()).not.toBe('Living room');
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

describe('adding a widget', () => {
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
    const choices = pk.findAll('.pk-detail select option').map((o) => o.attributes('value'));
    expect(choices).toEqual([arr.slice('arr@'.length)]); // the other one is still on the board
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
    expect(pk.find('button[type="submit"]').attributes('disabled')).toBeDefined(); // a name first
    await pk.find('input[type="text"]').setValue('Router admin');
    await pk.find('input[type="url"]').setValue('https://router.lab.example');
    const bind = pk.findAll('.pk-detail select')[0]!;
    expect(bind.findAll('optgroup').map((g) => g.attributes('label'))).toEqual(expect.arrayContaining(['Integrations', 'Services and containers']));
    await bind.setValue('instance:opnsense');
    await pk.find('form').trigger('submit');
    await flushPromises();

    const [board, tiles] = store.save.mock.lastCall!;
    const id = ids(board).find((i) => i.startsWith('service@tile:'))!;
    expect(id).toBeDefined();
    expect(tiles).toEqual({ [id]: { name: 'Router admin', href: 'https://router.lab.example', bind: { instance: 'opnsense' } } });
    expect(w.find(`[data-item-id="${id}"] .tn`).text()).toBe('Router admin');
  });
});
