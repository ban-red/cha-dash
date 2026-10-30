import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { remove } from '@chadash/layout';
import WidgetPicker from '../src/board/WidgetPicker.vue';
import { WIDGET_GROUPS, WIDGETS } from '../src/model/widgets';
import { setDisplayTimeZone } from '../src/lib/format';
import { applyTheme } from '../src/lib/theme';
import { button, editing, fakeStore, ids, mountSurface, settle, start } from './edit-mode';

beforeEach(() => {
  setDisplayTimeZone('UTC');
  applyTheme('instrument-dark');
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
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
    expect(pk.find('.picker .dlg-foot .sr').text()).toBe('Give it a name first.');
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
