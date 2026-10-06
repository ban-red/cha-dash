import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { nextTick } from 'vue';
import CommandPalette from '../src/components/CommandPalette.vue';
import { matchWord, rank, score } from '../src/lib/fuzzy';
import { route, syncRoute } from '../src/lib/router';
import { currentTheme } from '../src/lib/theme';
import { buildCommands, searchCommands, type CommandContext } from '../src/model/commands';
import { resetIntegrations } from '../src/stores/integrations';
import { docs, withIncidents } from './fixtures';
import { harness } from './harness';

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

beforeEach(() => {
  history.replaceState(null, '', '/');
  syncRoute();
  resetIntegrations();
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

describe('fuzzy matching', () => {
  it('ranks exact, prefix, word start, substring, then letters in order', () => {
    expect(matchWord('plex', 'plex')).toBe(100);
    expect(matchWord('jup', 'jupiter')).toBe(80);
    expect(matchWord('nas', 'jupiter-nas')).toBe(60);
    expect(matchWord('pit', 'jupiter')).toBe(40);
    expect(matchWord('jpt', 'jupiter')).toBeGreaterThan(0);
    expect(matchWord('jpt', 'jupiter')).toBeLessThan(40);
    expect(matchWord('xyz', 'jupiter')).toBe(0);
  });

  it('needs every word, weighs the label above keywords, and keeps ties in order', () => {
    expect(score('home assistant', { label: 'Home Assistant' })).toBeGreaterThan(score('home assistant', { label: 'Assistant', keywords: 'home' }));
    expect(score('home vm', { label: 'Home Assistant', keywords: 'app' })).toBe(0);
    const items = [
      { label: 'Sonarr', keywords: 'app:sonarr service' },
      { label: 'Docker engine on jupiter', keywords: 'engine' },
      { label: 'jupiter', keywords: 'host:jupiter host' },
    ];
    expect(rank('jupiter', items).map((i) => i.label)).toEqual(['jupiter', 'Docker engine on jupiter']);
    expect(rank('', items)).toHaveLength(3);
  });
});

describe('commands', () => {
  function context(over: Partial<CommandContext> = {}): CommandContext {
    const d = docs(withIncidents);
    return {
      graph: d.graph,
      inbox: d.inbox,
      boards: [
        { id: 'b1', name: 'Home' },
        { id: 'b2', name: 'Imported from Homepage' },
      ],
      currentBoard: 'b1',
      instances: [{ id: 'i-plex', integration: 'plex', name: 'Plex', status: 'repair' }],
      editing: false,
      theme: 'instrument-dark',
      username: 'owner',
      do: {
        openInbox: vi.fn(),
        toggleEdit: vi.fn(),
        openBoard: vi.fn(),
        reveal: vi.fn(),
        openSettings: vi.fn(),
        addIntegration: vi.fn(),
        setTheme: vi.fn(),
        signOut: vi.fn(),
      },
      ...over,
    };
  }

  it('lists page commands, boards, integrations and settings, and leaves resources for a query', () => {
    const groups = searchCommands(buildCommands(context()), '').map((s) => s.group);
    expect(groups).toEqual(['Actions', 'Inbox', 'Boards', 'Integrations', 'Settings', 'Theme', 'Account']);
  });

  it('searches the graph by name, kind and ID', () => {
    const all = buildCommands(context());
    const byName = searchCommands(all, 'jupiter').find((s) => s.group === 'Resources')!;
    expect(byName.items[0]!.label).toBe('jupiter');
    const byId = searchCommands(all, 'app:karakeep').find((s) => s.group === 'Resources')!;
    expect(byId.items[0]!.id).toBe('resource:app:karakeep');
    const byKind = searchCommands(all, 'camera').find((s) => s.group === 'Resources')!;
    expect(byKind.items.filter((c) => c.hint?.startsWith('camera')).length).toBeGreaterThanOrEqual(3);
    expect(byKind.items[0]!.hint).toMatch(/^camera/);
    expect(searchCommands(all, 'zzqx')).toEqual([]);
  });

  it('runs what it says: edit mode, themes, sign out, an integration', () => {
    const ctx = context();
    const all = buildCommands(ctx);
    all.find((c) => c.id === 'edit')!.run();
    expect(ctx.do.toggleEdit).toHaveBeenCalled();
    all.find((c) => c.id === 'theme:paper')!.run();
    expect(ctx.do.setTheme).toHaveBeenCalledWith('paper');
    all.find((c) => c.id === 'signout')!.run();
    expect(ctx.do.signOut).toHaveBeenCalled();
    all.find((c) => c.id === 'integration:i-plex')!.run();
    expect(ctx.do.openSettings).toHaveBeenCalledWith('integrations', 'i-plex');
    expect(buildCommands(context({ editing: true })).find((c) => c.id === 'edit')!.label).toBe('Done editing layout');
  });

  it('offers Add widget on a board, to those who may edit it', () => {
    const addWidget = vi.fn();
    const base = context();
    const ctx = context({ do: { ...base.do, addWidget } });
    const add = buildCommands(ctx).find((c) => c.id === 'add-widget')!;
    expect(add).toMatchObject({ group: 'Actions', label: 'Add widget' });
    add.run();
    expect(addWidget).toHaveBeenCalled();
    expect(searchCommands(buildCommands(ctx), 'add wid')[0]!.items[0]!.id).toBe('add-widget');
    const viewer = { inbox: true, settings: true, integrations: false, editBoards: false, panes: [] };
    expect(buildCommands({ ...ctx, can: viewer }).some((c) => c.id === 'add-widget')).toBe(false);
    expect(buildCommands({ ...ctx, currentBoard: '' }).some((c) => c.id === 'add-widget')).toBe(false);
    expect(buildCommands(base).some((c) => c.id === 'add-widget')).toBe(false);
  });
});

describe('command bar', () => {
  async function open() {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json(200, [{ id: 'i-plex', integration: 'plex', name: 'Plex', status: 'repair' }])));
    const h = harness(docs(withIncidents));
    h.board.state.boards = [
      { id: 'b1', name: 'Home' },
      { id: 'b2', name: 'Media wall' },
    ];
    h.board.state.boardId = 'b1';
    h.ui.openPalette();
    h.mountIn(CommandPalette);
    await flushPromises();
    const input = document.body.querySelector<HTMLInputElement>('.palette input')!;
    return { ...h, input };
  }

  const type = async (input: HTMLInputElement, text: string) => {
    input.value = text;
    input.dispatchEvent(new Event('input'));
    await nextTick();
  };
  const key = async (input: HTMLInputElement, k: string, init: KeyboardEventInit = {}) => {
    input.dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true, ...init }));
    await nextTick();
  };
  const selected = () => document.body.querySelector('[role="option"][aria-selected="true"]');

  it('is an ARIA combobox that keeps focus in the input', async () => {
    const { input } = await open();
    expect(document.activeElement).toBe(input);
    expect(input.getAttribute('role')).toBe('combobox');
    expect(input.getAttribute('aria-expanded')).toBe('true');
    const list = document.getElementById(input.getAttribute('aria-controls')!)!;
    expect(list.getAttribute('role')).toBe('listbox');
    expect(input.getAttribute('aria-activedescendant')).toBe(selected()!.id);
    expect(list.querySelectorAll('[role="group"]').length).toBeGreaterThan(3);
  });

  it('moves with the arrows (wrapping), runs with Enter and closes', async () => {
    const { input, ui } = await open();
    expect(selected()!.textContent).toContain('Open Inbox');
    await key(input, 'ArrowDown');
    expect(selected()!.textContent).toContain('Edit layout');
    expect(input.getAttribute('aria-activedescendant')).toBe(selected()!.id);
    await key(input, 'ArrowUp');
    await key(input, 'ArrowUp');
    expect(selected()!.textContent).toContain('Sign out'); // wrapped to the end
    await key(input, 'ArrowDown');
    await key(input, 'ArrowDown');
    await key(input, 'Enter');
    expect(ui.state.editing).toBe(true);
    expect(ui.state.paletteOpen).toBe(false);
  });

  it('opens edit mode with the widget picker from “Add widget”', async () => {
    const { input, ui } = await open();
    await type(input, 'add widget');
    expect(selected()!.textContent).toContain('Add widget');
    await key(input, 'Enter');
    expect(ui.state.paletteOpen).toBe(false);
    expect(ui.state.editing).toBe(true);
    expect(ui.state.addingWidget).toBe(true); // EditableBoard opens the picker and clears it
  });

  it('finds resources fuzzily and opens an incident in the Inbox', async () => {
    const { input, ui } = await open();
    await type(input, 'plex tokn');
    expect(selected()!.textContent).toContain('Plex rejected its token');
    await key(input, 'Enter');
    expect(ui.state.inboxOpen).toBe(true);
    expect(ui.state.inboxFocus).toBe('plex-token-revoked');
  });

  it('lists integrations from the hub and opens their settings', async () => {
    const { input } = await open();
    await type(input, 'plex');
    const groups = [...document.body.querySelectorAll('.pl .grp')].map((g) => g.textContent);
    expect(groups).toContain('Integrations');
    const opt = [...document.body.querySelectorAll<HTMLElement>('[role="option"]')].find((o) => o.textContent?.includes('Plex') && o.textContent.includes('plex'))!;
    opt.click();
    expect(route.value.path).toBe('/settings/integrations/i-plex');
  });

  it('switches boards and themes', async () => {
    const { input } = await open();
    await type(input, 'media wall');
    await key(input, 'Enter');
    expect(route.value.path).toBe('/boards/b2');
    await nextTick();
  });

  it('says so when nothing matches, and Esc closes', async () => {
    const { input, ui } = await open();
    await type(input, 'zzqx');
    expect(document.body.querySelector('.pl .none')!.textContent).toContain('No matches for “zzqx”');
    expect(input.hasAttribute('aria-activedescendant')).toBe(false);
    await key(input, 'Escape');
    expect(ui.state.paletteOpen).toBe(false);
  });

  it('changes the theme from a command', async () => {
    const { input } = await open();
    await type(input, 'theme paper');
    await key(input, 'Enter');
    expect(currentTheme.value).toBe('paper');
  });
});
