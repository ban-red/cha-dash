import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import TileEditor from '../src/board/TileEditor.vue';
import { setDisplayTimeZone } from '../src/lib/format';
import { navigate, route } from '../src/lib/router';
import { applyTheme } from '../src/lib/theme';
import { tileInstance } from '../src/model/integrations';
import { docs, withIncidents } from './fixtures';
import { fakeStore, mountSurface, settle, start } from './edit-mode';

beforeEach(() => {
  setDisplayTimeZone('UTC');
  applyTheme('instrument-dark');
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
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
    expect(items().map((e) => /^(Integration settings|Widget settings|Resize|Reorder|Move to section|Remove)/.exec(e.textContent?.trim() ?? '')?.[1])).toEqual(['Integration settings', 'Widget settings', 'Resize', 'Reorder', 'Move to section', 'Remove']);
    item('Integration settings').click();
    await settle();
    expect(route.value.path).toBe('/settings/integrations/plex');
    expect(w.ui.state.editing).toBe(false);
  });

  it('moves the tile to the end of another section', async () => {
    const store = fakeStore(start());
    const w = mountSurface(store);
    await settle();
    await w.find('[data-item="plex"] .tile-cog').trigger('click');
    await settle();
    item('Move to section').click();
    await settle();
    expect(items().map((e) => e.textContent?.trim())).toEqual(['Home', 'Infrastructure', 'Activity']);
    item('Activity').click();
    await settle();
    const board = store.board.value!;
    expect(board.sections.find((s) => s.id === 'media')!.items.some((i) => i.id === 'plex')).toBe(false);
    expect(board.sections.find((s) => s.id === 'activity')!.items.at(-1)!.id).toBe('plex');
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
