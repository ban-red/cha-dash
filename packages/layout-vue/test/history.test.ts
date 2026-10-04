import { nudge, type Board } from '@chadash/layout';
import { mount } from '@vue/test-utils';
import { afterEach, describe, expect, it } from 'vitest';
import { defineComponent, h, nextTick, ref } from 'vue';
import LayoutBoard from '../src/LayoutBoard.vue';
import { useLayoutHistory, type LayoutHistory } from '../src/useLayoutHistory.ts';
import { homeBoard, key } from './helpers.ts';

afterEach(() => {
  document.body.innerHTML = '';
});

describe('useLayoutHistory', () => {
  it('undoes and redoes commits from the board', async () => {
    const original = homeBoard();
    let history!: LayoutHistory;
    const Host = defineComponent({
      setup() {
        history = useLayoutHistory(original);
        return () =>
          h(
            LayoutBoard,
            { board: history.board.value, editing: true, breakpoint: 'desktop', motion: 'none', onCommit: history.commit },
            { item: ({ item }: { item: { id: string } }) => h('div', item.id) },
          );
      },
    });
    const wrapper = mount(Host, { attachTo: document.body });
    const root = wrapper.element as HTMLElement;
    const item = (id: string) => root.querySelector<HTMLElement>(`[data-item-id="${id}"]`) as HTMLElement;
    expect(history.canUndo.value).toBe(false);

    // Only @commit is wired: the board previews internally during the gesture.
    item('dns').focus();
    key(item('dns'), 'Enter');
    key(item('dns'), 'ArrowLeft');
    await nextTick();
    expect(item('dns').dataset['x']).toBe(String(nudge(original, 'dns', 'left').sections[0]?.items.find((i) => i.id === 'dns')?.x));
    expect(history.canUndo.value).toBe(false);
    key(item('dns'), 'Enter');
    await nextTick();
    const committed = history.board.value;
    expect(committed).toEqual(nudge(original, 'dns', 'left'));
    expect(history.canUndo.value).toBe(true);
    expect(item('dns').dataset['x']).toBe('4');

    expect(history.undo()).toBe(original);
    await nextTick();
    expect(history.board.value).toBe(original);
    expect(history.canRedo.value).toBe(true);
    expect(item('dns').dataset['x']).toBe('4');
    expect(item('dns').dataset['y']).toBe('2');

    expect(history.redo()).toBe(committed);
    await nextTick();
    expect(history.board.value).toBe(committed);
    expect(history.canRedo.value).toBe(false);
  });

  it('ignores no-op commits and respects the limit', () => {
    const a = homeBoard();
    const history = useLayoutHistory(a, { limit: 2 });
    history.commit(a);
    expect(history.canUndo.value).toBe(false);
    const b = nudge(a, 'dns', 'left');
    const c = nudge(b, 'clock', 'right');
    const d = nudge(c, 'sonarr', 'left');
    expect(new Set([a, b, c, d]).size).toBe(4);
    history.commit(b);
    history.commit(c);
    history.commit(d);
    expect(history.history.value.past).toHaveLength(2);
    expect(history.undo()).toBe(c);
    expect(history.undo()).toBe(b);
    expect(history.undo()).toBe(b);
    expect(history.canUndo.value).toBe(false);
  });

  it('starts over when a ref source changes to a new board, and keeps raw objects', async () => {
    const first = homeBoard();
    const source = ref<Board>(first); // a deep ref: reads come back as proxies
    const history = useLayoutHistory(source);
    expect(history.board.value).toBe(first);
    const next = nudge(first, 'dns', 'left');
    history.commit(next);
    expect(history.canUndo.value).toBe(true);
    const loaded = homeBoard();
    source.value = loaded;
    await nextTick();
    expect(history.board.value).toBe(loaded);
    expect(history.canUndo.value).toBe(false);
    history.reset(first);
    expect(history.board.value).toBe(first);
  });
});
