import { deriveLayout, nudge, resize, sectionColumns, stepSize, type Board } from '@chadash/layout';
import { afterEach, describe, expect, it } from 'vitest';
import { nextTick } from 'vue';
import { expectValid, homeBoard, key, mountBoard } from './helpers.ts';

afterEach(() => {
  document.body.innerHTML = '';
});

async function settle(): Promise<void> {
  await nextTick();
  await nextTick();
}

function placedAt(board: Board, id: string, breakpoint: 'desktop' | 'tablet' | 'phone' = 'desktop') {
  for (const section of deriveLayout(board, breakpoint).sections) {
    const placed = section.items.find((it) => it.id === id);
    if (placed !== undefined) return { section: section.id, ...placed };
  }
  throw new Error(`no ${id}`);
}

describe('keyboard', () => {
  it('uses a roving tabindex while editing and none while viewing', async () => {
    const h = mountBoard({ board: homeBoard(), breakpoint: 'desktop', editing: true });
    const stops = [...(h.wrapper.element as HTMLElement).querySelectorAll<HTMLElement>('[data-item-id]')].map((el) => el.tabIndex);
    expect(stops.filter((t) => t === 0)).toHaveLength(1);
    expect(h.item('pve').tabIndex).toBe(0);
    expect(h.item('pve').getAttribute('role')).toBe('button');
    expect(h.item('pve').getAttribute('aria-roledescription')).toBe('movable widget');
    expect(h.item('pve').getAttribute('aria-label')).toBe('Proxmox, Large');

    h.item('pve').focus();
    key(h.item('pve'), 'ArrowRight');
    await settle();
    expect(document.activeElement).toBe(h.item('nas'));
    expect(h.item('nas').tabIndex).toBe(0);
    expect(h.item('pve').tabIndex).toBe(-1);
    expect(h.selects).toEqual(['pve', 'nas']);

    key(h.item('nas'), 'ArrowDown');
    await settle();
    expect(document.activeElement).toBe(h.item('dns'));
    key(h.item('dns'), 'End');
    await settle();
    expect(document.activeElement).toBe(h.item('sonarr'));
    key(h.item('sonarr'), 'Home');
    await settle();
    expect(document.activeElement).toBe(h.item('pve'));
    // Arrows only move focus: nothing was emitted.
    expect(h.updates).toEqual([]);

    await h.wrapper.setProps({ editing: false });
    expect(h.item('pve').hasAttribute('tabindex')).toBe(false);
    expect(h.item('pve').hasAttribute('role')).toBe(false);
  });

  it('picks up, nudges and drops with Enter and arrows', async () => {
    const board = homeBoard();
    const h = mountBoard({ board, breakpoint: 'desktop', editing: true });
    const el = h.item('plex');
    el.focus();
    key(el, 'Enter');
    await settle();
    expect(el.getAttribute('aria-pressed')).toBe('true');
    expect(el.classList.contains('is-picked')).toBe(true);
    expect(el.querySelector('[data-widget]')?.getAttribute('data-dragging')).toBe('true');

    key(h.item('plex'), 'ArrowLeft');
    await settle();
    const once = nudge(board, 'plex', 'left');
    expect(h.updates).toEqual([once]);
    expect(h.commits).toEqual([]);
    expect(document.activeElement).toBe(h.item('plex'));

    key(h.item('plex'), 'ArrowUp');
    await settle();
    const twice = nudge(once, 'plex', 'up');
    expect(h.updates).toEqual([once, twice]);

    key(h.item('plex'), ' ');
    await settle();
    expect(h.commits).toEqual([twice]);
    expect(h.item('plex').getAttribute('aria-pressed')).toBe('false');
    // The parent holds exactly the committed object.
    expect(h.board()).toBe(h.commits[0]);
    expectValid(h.updates);
  });

  it('crosses into the next section at the edge on desktop', async () => {
    const board = homeBoard();
    const h = mountBoard({ board, breakpoint: 'desktop', editing: true });
    h.item('plex').focus();
    key(h.item('plex'), 'Enter');
    key(h.item('plex'), 'ArrowRight');
    await settle();
    const next = nudge(board, 'plex', 'right');
    expect(h.updates).toEqual([next]);
    expect(placedAt(next, 'plex').section).toBe('side');
    expect(h.item('plex').dataset['section']).toBe('side');
    expect(document.activeElement).toBe(h.item('plex'));
    key(h.item('plex'), 'Enter');
    expect(h.commits).toEqual([next]);
  });

  it('resizes with Shift+arrows, as a one-shot operation when nothing is picked up', async () => {
    const board = homeBoard();
    const h = mountBoard({ board, breakpoint: 'desktop', editing: true });
    h.item('plex').focus();
    key(h.item('plex'), 'ArrowRight', { shiftKey: true });
    await settle();
    const wider = resize(board, 'plex', 'wide');
    expect(stepSize(['small', 'wide', 'large'], 'small', 'wider', 8)).toBe('wide');
    expect(h.updates).toEqual([wider]);
    expect(h.commits).toEqual([wider]);
    expect(h.item('plex').dataset['size']).toBe('wide');

    key(h.item('plex'), 'ArrowDown', { shiftKey: true });
    await settle();
    const taller = resize(wider, 'plex', 'large');
    expect(h.updates).toEqual([wider, taller]);
    expect(h.commits).toEqual([wider, taller]);

    // No larger supported size: nothing emitted, and it says so.
    key(h.item('plex'), 'ArrowDown', { shiftKey: true });
    await settle();
    expect(h.updates).toHaveLength(2);
    expect(h.announcement()).toBe("Plex can't get any taller");
    expectValid(h.updates);
  });

  it('resizes inside a pick-up as part of the gesture', async () => {
    const board = homeBoard();
    const h = mountBoard({ board, breakpoint: 'desktop', editing: true });
    h.item('dns').focus();
    key(h.item('dns'), 'Enter');
    key(h.item('dns'), 'ArrowRight', { shiftKey: true });
    await settle();
    expect(h.updates).toEqual([resize(board, 'dns', 'wide')]);
    expect(h.commits).toEqual([]);
    key(h.item('dns'), 'Enter');
    expect(h.commits).toEqual([resize(board, 'dns', 'wide')]);
  });

  it('edits the derived layout at phone, as overrides', async () => {
    const board = homeBoard();
    const h = mountBoard({ board, breakpoint: 'phone', editing: true });
    h.item('dns').focus();
    key(h.item('dns'), 'Enter');
    key(h.item('dns'), 'ArrowLeft');
    await settle();
    const moved = nudge(board, 'dns', 'left', { breakpoint: 'phone' });
    expect(h.updates).toEqual([moved]);
    // The canonical layout is untouched; only the phone order changed.
    expect(moved.sections[0]?.items).toBe(board.sections[0]?.items);
    key(h.item('dns'), 'ArrowRight', { shiftKey: true });
    await settle();
    const section = moved.sections[0];
    if (section === undefined) throw new Error('no section');
    const size = stepSize(['small', 'wide'], 'small', 'wider', sectionColumns(section, 'phone'));
    expect(size).toBe('wide');
    expect(h.updates[1]).toEqual(resize(moved, 'dns', 'wide', { breakpoint: 'phone' }));
    key(h.item('dns'), 'Enter');
    expect(h.commits).toHaveLength(1);
    expectValid(h.updates);
  });

  it('Escape cancels a pick-up and restores the original board', async () => {
    const board = homeBoard();
    const h = mountBoard({ board, breakpoint: 'desktop', editing: true });
    h.item('dns').focus();
    key(h.item('dns'), 'Enter');
    key(h.item('dns'), 'ArrowLeft');
    await settle();
    key(h.item('dns'), 'ArrowLeft');
    await settle();
    expect(h.updates).toHaveLength(2);
    key(h.item('dns'), 'Escape');
    await settle();
    expect(h.updates).toHaveLength(3);
    expect(h.updates[2]).toBe(board);
    expect(h.board()).toBe(board);
    expect(h.commits).toEqual([]);
    expect(h.announcement()).toBe('Cancelled. Pi-hole is back at row 3, column 5');
    expect(document.activeElement).toBe(h.item('dns'));
  });

  it('Escape with nothing picked up is left to the app', () => {
    const h = mountBoard({ board: homeBoard(), breakpoint: 'desktop', editing: true });
    h.item('dns').focus();
    const event = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true });
    h.item('dns').dispatchEvent(event);
    expect(event.defaultPrevented).toBe(false);
  });

  it('drops when focus leaves the picked-up item', async () => {
    const board = homeBoard();
    const h = mountBoard({ board, breakpoint: 'desktop', editing: true });
    h.item('dns').focus();
    key(h.item('dns'), 'Enter');
    key(h.item('dns'), 'ArrowLeft');
    await settle();
    const outside = document.createElement('button');
    document.body.append(outside);
    outside.focus();
    await new Promise((r) => setTimeout(r, 5));
    expect(h.commits).toEqual([nudge(board, 'dns', 'left')]);
  });
});
