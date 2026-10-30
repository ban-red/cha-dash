import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { defineComponent, h } from 'vue';
import { LayoutBoard } from '@chadash/layout-vue';
import { remove, resize } from '@chadash/layout';
import BoardSurface from '../src/board/BoardSurface.vue';
import TileEditor from '../src/board/TileEditor.vue';
import { setDisplayTimeZone } from '../src/lib/format';
import { applyTheme } from '../src/lib/theme';
import { provideBoard, type BoardStore } from '../src/stores/board';
import type { TilesDoc } from '../src/stores/tiles';
import { docs, withIncidents } from './fixtures';
import { fakeFetch } from './fake-fetch';
import { harness } from './harness';
import { button, editing, fakeStore, ids, mountSurface, select, settle, start } from './edit-mode';

beforeEach(() => {
  setDisplayTimeZone('UTC');
  applyTheme('instrument-dark');
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

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
