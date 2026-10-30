/** What the edit-mode test files share: a fake board store and the surface mounted in edit mode. */
import { vi } from 'vitest';
import { flushPromises, type VueWrapper } from '@vue/test-utils';
import { defineComponent, h, reactive, shallowRef } from 'vue';
import { LayoutBoard } from '@chadash/layout-vue';
import { type Board } from '@chadash/layout';
import BoardSurface from '../src/board/BoardSurface.vue';
import { defaultBoard } from '../src/model/defaultLayout';
import { provideBoard, type BoardStore } from '../src/stores/board';
import type { TilesDoc } from '../src/stores/tiles';
import { docs, withIncidents } from './fixtures';
import { harness } from './harness';

/** A board store that keeps the saved board and tiles, like the real one after a 200. */
export function fakeStore(initial: Board, save: (b: Board) => Promise<boolean> = async () => true, tiles: TilesDoc = {}) {
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

export function mountSurface(store: BoardStore) {
  const hs = harness(docs(withIncidents));
  const Page = defineComponent({
    setup() {
      provideBoard(store);
      return () => h(BoardSurface, { board: store.board.value!, breakpoint: 'desktop' });
    },
  });
  return Object.assign(hs.mountIn(Page), { ui: hs.ui, discovery: hs.discovery, session: hs.session });
}

/** Edit mode, the picker and the editor load on demand: wait for the import, then for Vue. */
export async function settle() {
  await vi.dynamicImportSettled();
  await flushPromises();
}

export const start = () => defaultBoard(docs(withIncidents).graph, Date.parse(withIncidents.now));
export const button = (w: VueWrapper, text: string) => w.findAll('button').find((b) => b.text() === text)!;
export const ids = (b: Board) => b.sections.flatMap((s) => s.items.map((i) => i.id));

export async function editing(store: BoardStore) {
  const w = mountSurface(store);
  w.ui.state.editing = true;
  await settle();
  return w;
}

/** Select a tile the way focusing it does. */
export async function select(w: VueWrapper, id: string) {
  w.findComponent(LayoutBoard).vm.$emit('select', id);
  await flushPromises();
}
