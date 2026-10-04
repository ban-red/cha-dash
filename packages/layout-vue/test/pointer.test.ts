import { move, resize, type Board } from '@chadash/layout';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { nextTick } from 'vue';
import { MOMENTUM, projectMomentum, projectionTime } from '../src/momentum.ts';
import { PITCH, drag, expectValid, homeBoard, mountBoard, pointer, rowBoard, stubGeometry } from './helpers.ts';

let restore: () => void;

beforeEach(() => {
  restore = stubGeometry();
});

afterEach(() => {
  restore();
  document.body.innerHTML = '';
});

function xOf(board: Board, id: string): number {
  for (const section of board.sections) {
    const item = section.items.find((it) => it.id === id);
    if (item !== undefined) return item.x;
  }
  throw new Error(`no ${id}`);
}

describe('pointer drag', () => {
  it('previews a live reflow while dragging, from the board it started on', async () => {
    const board = rowBoard();
    const h = mountBoard({ board, breakpoint: 'desktop', editing: true });
    const el = h.item('plex');
    // plex is 2×2 at (0,0); nas 4×2 at (2,0); dns 2×2 at (6,0). Grab plex 50 px in.
    pointer(el, 'pointerdown', 50, 50, 0);
    pointer(window, 'pointermove', 52, 50, 10); // under the threshold: nothing yet
    expect(h.updates).toEqual([]);
    pointer(window, 'pointermove', 350, 50, 500); // top-left at 300 px → cell 3
    await nextTick();
    expect(h.updates).toEqual([move(board, 'plex', { sectionId: 'main', x: 3, y: 0 })]);
    expect(h.item('nas').dataset['x']).not.toBe('2'); // the neighbour reflowed
    expect(h.item('plex').classList.contains('is-lifted')).toBe(true);
    expect(h.wrapper.element.querySelector('.layout-drop')).not.toBeNull();
    expect(h.commits).toEqual([]);

    pointer(window, 'pointermove', 50, 50, 1500); // back home: the preview is the origin again
    await nextTick();
    expect(h.updates[h.updates.length - 1]).toBe(board);
    pointer(window, 'pointerup', 50, 50, 2000);
    await nextTick();
    expect(h.commits).toEqual([]); // dropped where it started: nothing to commit
    expect(h.wrapper.element.querySelector('.layout-drop')).toBeNull();
    expectValid(h.updates);
  });

  it('drops a slow drag where the pointer is', async () => {
    const board = rowBoard();
    const h = mountBoard({ board, breakpoint: 'desktop', editing: true });
    // 300 px over 3 s is 100 px/s, inside the dead zone: no projection.
    drag(h.item('plex'), { x: 50, y: 50 }, { x: 350, y: 50 }, { duration: 3000, steps: 30 });
    await nextTick();
    const expected = move(board, 'plex', { sectionId: 'main', x: 3, y: 0 });
    expect(h.commits).toHaveLength(1);
    expect(h.commits[0]).toEqual(expected);
    expect(h.board()).toBe(h.commits[0]);
    // The engine lands on the nearest free cell to (3, 0): just after the wide NAS.
    expect(xOf(expected, 'plex')).toBe(4);
    expect(h.announcement()).toBe('Moved Plex to row 1, column 5');
    expectValid(h.updates);
  });

  it('projects a fast flick further than a slow drag, to the projected cell', async () => {
    const board = rowBoard();
    const slow = mountBoard({ board, breakpoint: 'desktop', editing: true });
    drag(slow.item('plex'), { x: 50, y: 50 }, { x: 350, y: 50 }, { duration: 3000, steps: 30 });
    await nextTick();
    slow.wrapper.unmount();

    const fast = mountBoard({ board, breakpoint: 'desktop', editing: true });
    // The same 300 px in 100 ms is 3000 px/s.
    drag(fast.item('plex'), { x: 50, y: 50 }, { x: 350, y: 50 }, { duration: 100, steps: 10 });
    await nextTick();

    const push = projectMomentum({ x: 3000, y: 0 }, PITCH);
    // (3000 − 250) px/s × 0.199 s = 547 px, capped at 4 cells.
    expect(projectionTime()).toBeCloseTo(0.199, 3);
    expect(push.x).toBeCloseTo(MOMENTUM.maxCells * PITCH, 6);
    const projectedCell = Math.round((300 + push.x) / PITCH);
    expect(projectedCell).toBe(7);
    const expected = move(board, 'plex', { sectionId: 'main', x: projectedCell, y: 0 });
    expect(fast.commits).toHaveLength(1);
    expect(fast.commits[0]).toEqual(expected);
    expect(xOf(fast.commits[0] as Board, 'plex')).toBeGreaterThan(xOf(slow.commits[0] as Board, 'plex'));
    expectValid([...slow.updates, ...fast.updates]);
  });

  it('ignores velocity when the pointer rested before letting go', async () => {
    const board = rowBoard();
    const h = mountBoard({ board, breakpoint: 'desktop', editing: true });
    // Fast, then 300 ms still before release: no samples in the velocity window.
    drag(h.item('plex'), { x: 50, y: 50 }, { x: 350, y: 50 }, { duration: 100, steps: 10, rest: 300 });
    await nextTick();
    expect(h.commits[0]).toEqual(move(board, 'plex', { sectionId: 'main', x: 3, y: 0 }));
  });

  it('moves an item into another section without remounting its widget', async () => {
    const board = homeBoard();
    const h = mountBoard({ board, breakpoint: 'desktop', editing: true });
    const el = h.item('dns');
    const widget = el.querySelector('[data-widget]');
    // dns is at infra (4,2): box left 400, top 200. The side section starts at 800.
    drag(el, { x: 450, y: 250 }, { x: 850, y: 50 }, { duration: 4000, steps: 40 });
    await nextTick();
    const expected = move(board, 'dns', { sectionId: 'side', x: 0, y: 0 });
    expect(h.commits[0]).toEqual(expected);
    expect(h.item('dns')).toBe(el);
    expect(h.item('dns').querySelector('[data-widget]')).toBe(widget);
    expect(h.item('dns').dataset['section']).toBe('side');
    expect(h.announcement()).toBe('Moved Pi-hole to Today, row 1, column 1');
    expectValid(h.updates);
  });

  it('keeps drags inside the section at derived breakpoints', async () => {
    const board = homeBoard();
    const h = mountBoard({ board, breakpoint: 'phone', editing: true });
    const el = h.item('dns');
    const box = el.getBoundingClientRect();
    // Drag down and right, a long way: over the next section's band.
    drag(el, { x: box.left + 20, y: box.top + 20 }, { x: box.left + 220, y: box.top + 1400 }, { duration: 4000, steps: 40 });
    await nextTick();
    expect(h.commits).toHaveLength(1);
    const committed = h.commits[0] as Board;
    expect(committed.sections[0]?.items.map((it) => it.id)).toContain('dns');
    // Only the phone order changed; the canonical layout is untouched.
    expect(committed.sections[0]?.items).toBe(board.sections[0]?.items);
    expect(committed.sections[0]?.overrides?.phone?.order).toBeDefined();
    expectValid(h.updates);
  });

  it('Escape cancels a drag and restores the original board', async () => {
    const board = rowBoard();
    const h = mountBoard({ board, breakpoint: 'desktop', editing: true });
    pointer(h.item('plex'), 'pointerdown', 50, 50, 0);
    pointer(window, 'pointermove', 450, 50, 500);
    await nextTick();
    expect(h.updates).toHaveLength(1);
    const escape = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true });
    window.dispatchEvent(escape);
    await nextTick();
    expect(escape.defaultPrevented).toBe(true);
    expect(h.updates[h.updates.length - 1]).toBe(board);
    expect(h.board()).toBe(board);
    expect(h.commits).toEqual([]);
    expect(h.announcement()).toBe('Cancelled. Plex is back at row 1, column 1');
    // The rest of the gesture is ignored.
    pointer(window, 'pointermove', 650, 50, 600);
    pointer(window, 'pointerup', 650, 50, 700);
    expect(h.commits).toEqual([]);
  });

  it('pointercancel cancels too', async () => {
    const board = rowBoard();
    const h = mountBoard({ board, breakpoint: 'desktop', editing: true });
    pointer(h.item('plex'), 'pointerdown', 50, 50, 0);
    pointer(window, 'pointermove', 450, 50, 500);
    pointer(window, 'pointercancel', 450, 50, 510);
    await nextTick();
    expect(h.updates[h.updates.length - 1]).toBe(board);
    expect(h.commits).toEqual([]);
  });

  it('does nothing outside edit mode', () => {
    const h = mountBoard({ board: rowBoard(), breakpoint: 'desktop', editing: false });
    drag(h.item('plex'), { x: 50, y: 50 }, { x: 450, y: 50 }, { duration: 100 });
    expect(h.updates).toEqual([]);
    expect(h.commits).toEqual([]);
  });
});

describe('resize handle', () => {
  it('snaps to supported sizes as the corner moves', async () => {
    const board = rowBoard();
    const h = mountBoard({ board, breakpoint: 'desktop', editing: true });
    const handle = h.item('plex').querySelector('.layout-handle') as HTMLElement;
    expect(handle.textContent?.trim()).toBe('2×2');
    pointer(handle, 'pointerdown', 180, 180, 0);
    pointer(window, 'pointermove', 230, 180, 100); // 2.5 × 2: still small
    expect(h.updates).toEqual([]);
    pointer(window, 'pointermove', 380, 190, 200); // 4 × 2.1: wide
    await nextTick();
    const wide = resize(board, 'plex', 'wide');
    expect(h.updates).toEqual([wide]);
    expect(h.item('plex').dataset['size']).toBe('wide');
    expect((h.item('plex').querySelector('.layout-handle') as HTMLElement).textContent?.trim()).toBe('4×2');
    expect(h.item('plex').querySelector('.layout-resize-ghost')).not.toBeNull();
    pointer(window, 'pointermove', 380, 380, 300); // 4 × 4: large
    await nextTick();
    expect(h.updates[1]).toEqual(resize(board, 'plex', 'large'));
    pointer(window, 'pointerup', 380, 380, 400);
    await nextTick();
    expect(h.commits).toEqual([h.updates[1]]);
    expect(h.announcement()).toBe('Resized to Large');
    expect(h.item('plex').querySelector('.layout-resize-ghost')).toBeNull();
    expectValid(h.updates);
  });

  it('never picks a size the widget does not support', async () => {
    const board = rowBoard();
    const h = mountBoard({ board, breakpoint: 'desktop', editing: true });
    // dns supports small and wide only. Pull the corner out to 8 × 4.
    const handle = h.item('dns').querySelector('.layout-handle') as HTMLElement;
    pointer(handle, 'pointerdown', 780, 180, 0);
    pointer(window, 'pointermove', 1380, 380, 100);
    pointer(window, 'pointerup', 1380, 380, 200);
    await nextTick();
    expect(h.commits).toEqual([resize(board, 'dns', 'wide')]);
    for (const b of h.updates) {
      const dns = b.sections[0]?.items.find((it) => it.id === 'dns');
      expect(['small', 'wide']).toContain(dns?.size);
    }
  });

  it('Escape cancels a resize', async () => {
    const board = rowBoard();
    const h = mountBoard({ board, breakpoint: 'desktop', editing: true });
    const handle = h.item('plex').querySelector('.layout-handle') as HTMLElement;
    pointer(handle, 'pointerdown', 180, 180, 0);
    pointer(window, 'pointermove', 380, 380, 100);
    await nextTick();
    expect(h.updates).toHaveLength(1);
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
    await nextTick();
    expect(h.updates[h.updates.length - 1]).toBe(board);
    expect(h.commits).toEqual([]);
    expect(h.item('plex').dataset['size']).toBe('small');
  });
});
