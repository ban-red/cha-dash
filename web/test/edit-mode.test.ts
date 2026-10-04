import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { defineComponent, h, reactive, shallowRef } from 'vue';
import { LayoutBoard } from '@chadash/layout-vue';
import { resize, type Board } from '@chadash/layout';
import BoardSurface from '../src/board/BoardSurface.vue';
import { defaultBoard } from '../src/model/defaultLayout';
import { setDisplayTimeZone } from '../src/lib/format';
import { applyTheme } from '../src/lib/theme';
import { provideBoard, type BoardStore } from '../src/stores/board';
import { docs, withIncidents } from './fixtures';
import { harness } from './harness';

beforeEach(() => {
  setDisplayTimeZone('UTC');
  applyTheme('instrument-dark');
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

/** A board store that keeps the saved board, like the real one after a 200. */
function fakeStore(initial: Board, save: (b: Board) => Promise<boolean> = async () => true) {
  const board = shallowRef<Board | undefined>(initial);
  const store = {
    state: reactive({ unsaved: false }),
    board,
    load: vi.fn(),
    save: vi.fn(async (b: Board) => {
      board.value = b;
      return save(b);
    }),
  };
  return store as unknown as BoardStore & { save: typeof store.save };
}

function mountSurface(store: BoardStore) {
  const { mountIn, ui } = harness(docs(withIncidents));
  const Page = defineComponent({
    setup() {
      provideBoard(store);
      return () => h(BoardSurface, { board: store.board.value!, breakpoint: 'desktop' });
    },
  });
  return Object.assign(mountIn(Page), { ui });
}

/** Edit mode loads on demand: wait for the import, then for Vue. */
async function settle() {
  await vi.dynamicImportSettled();
  await flushPromises();
}

const start = () => defaultBoard(docs(withIncidents).graph, Date.parse(withIncidents.now));

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
    const w = mountSurface(store);
    await w.find('.board-tools button').trigger('click');
    await settle();

    const item = before.sections.flatMap((s) => s.items).find((i) => i.supportedSizes.length > 1 && i.size !== i.supportedSizes[0])!;
    const after = resize(before, item.id, item.supportedSizes[0]!);
    w.findComponent(LayoutBoard).vm.$emit('commit', after);
    await flushPromises();
    expect(store.save).toHaveBeenLastCalledWith(after);

    const undo = w.findAll('.edit-bar button').find((b) => b.text() === 'Undo')!;
    expect(undo.attributes('disabled')).toBeUndefined();
    await undo.trigger('click');
    await flushPromises();
    expect(store.save).toHaveBeenLastCalledWith(before);
  });

  it('tells you when someone else saved first', async () => {
    const before = start();
    const w = mountSurface(fakeStore(before, async () => false));
    await w.find('.board-tools button').trigger('click');
    await settle();

    const item = before.sections.flatMap((s) => s.items).find((i) => i.supportedSizes.length > 1 && i.size !== i.supportedSizes[0])!;
    w.findComponent(LayoutBoard).vm.$emit('commit', resize(before, item.id, item.supportedSizes[0]!));
    await flushPromises();
    expect(w.ui.state.toasts.map((t) => t.text).join()).toContain('This board changed somewhere else');
  });
});
