import { describe, expect, test } from 'bun:test';
import {
  DEFAULT_HISTORY_LIMIT,
  canRedo,
  canUndo,
  commit,
  createBoard,
  createHistory,
  move,
  redo,
  resize,
  undo,
} from '../src/index.ts';
import { item, section } from './helpers.ts';

describe('history', () => {
  test('starts empty with the default cap', () => {
    const h = createHistory('a');
    expect(h).toEqual({ past: [], present: 'a', future: [], limit: DEFAULT_HISTORY_LIMIT });
    expect(canUndo(h)).toBe(false);
    expect(canRedo(h)).toBe(false);
    expect(() => createHistory('a', { limit: 0 })).toThrow(RangeError);
    expect(() => createHistory('a', { limit: 1.5 })).toThrow(RangeError);
  });

  test('commit, undo, redo', () => {
    let h = createHistory('a');
    h = commit(h, 'b');
    h = commit(h, 'c');
    expect(h.past).toEqual(['a', 'b']);
    h = undo(h);
    expect(h.present).toBe('b');
    expect(h.future).toEqual(['c']);
    expect(canRedo(h)).toBe(true);
    h = redo(h);
    expect(h.present).toBe('c');
    expect(canRedo(h)).toBe(false);
  });

  test('committing the same state is a no-op', () => {
    const h = commit(createHistory('a'), 'b');
    expect(commit(h, 'b')).toBe(h);
  });

  test('a new commit after undo drops the redo stack', () => {
    let h = commit(commit(createHistory('a'), 'b'), 'c');
    h = undo(undo(h));
    h = commit(h, 'x');
    expect(h).toEqual({ past: ['a'], present: 'x', future: [], limit: DEFAULT_HISTORY_LIMIT });
  });

  test('the cap drops the oldest entries', () => {
    let h = createHistory(0, { limit: 3 });
    for (let i = 1; i <= 5; i++) h = commit(h, i);
    expect(h.past).toEqual([2, 3, 4]);
    h = undo(undo(undo(h)));
    expect(h.present).toBe(2);
    expect(canUndo(h)).toBe(false);
    expect(undo(h)).toBe(h);
  });

  test('undo and redo with nothing to do return the same history', () => {
    const h = createHistory('a');
    expect(undo(h)).toBe(h);
    expect(redo(h)).toBe(h);
  });

  test('works with boards: undo restores the exact previous board object', () => {
    const b0 = createBoard({ sections: [section('s', [item('a', 'small', ['small', 'wide']), item('b', 'small')])] });
    const b1 = move(b0, 'a', { x: 2, y: 0 });
    const b2 = resize(b1, 'a', 'wide');
    let h = commit(commit(createHistory(b0), b1), b2);
    h = undo(h);
    expect(h.present).toBe(b1);
    h = undo(h);
    expect(h.present).toBe(b0);
    h = redo(redo(h));
    expect(h.present).toBe(b2);
  });
});
