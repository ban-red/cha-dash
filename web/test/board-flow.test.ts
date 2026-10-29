/**
 * Board flows: Sections (bands), Bento (sections packed densely) and Mosaic
 * (every tile packed on the board). The read-only board draws each from the
 * engine's track areas; edit mode picks the flow and shows a mosaic as bento.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, type VueWrapper } from '@vue/test-utils';
import { defineComponent, h, reactive, shallowRef } from 'vue';
import { LayoutBoard } from '@chadash/layout-vue';
import { deriveLayout, setFlow, type Board } from '@chadash/layout';
import BoardSurface from '../src/board/BoardSurface.vue';
import StaticBoard from '../src/board/StaticBoard.vue';
import { defaultBoard } from '../src/model/defaultLayout';
import { setDisplayTimeZone } from '../src/lib/format';
import { applyTheme } from '../src/lib/theme';
import { provideBoard, type BoardStore } from '../src/stores/board';
import { axeViolations } from './axe';
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

const start = () => defaultBoard(docs(withIncidents).graph, Date.parse(withIncidents.now));
const css = (w: VueWrapper | ReturnType<VueWrapper['find']>) => (w.element as HTMLElement).style;
const rows = (a: { track: number; tracks: number }) => `${a.track + 1} / span ${a.tracks}`;

describe('the read-only board in each flow', () => {
  it('keeps bands for Sections', () => {
    const w = harness(docs(withIncidents)).mountIn(StaticBoard, { board: start(), breakpoint: 'desktop' });
    expect(w.find('.board-flow').exists()).toBe(false);
    expect(w.find('.board').attributes('data-flow')).toBeUndefined();
  });

  it('places Bento sections as blocks on half-row tracks', () => {
    const board = setFlow(start(), 'bento');
    const w = harness(docs(withIncidents)).mountIn(StaticBoard, { board, breakpoint: 'desktop' });
    const layout = deriveLayout(board, 'desktop', { headers: 'always' });
    const grid = w.find('.board-flow > .board');
    expect(grid.attributes('data-flow')).toBe('bento');
    expect(css(grid).gridTemplateRows).toBe(`repeat(${layout.tracks}, var(--track))`);
    const secs = w.findAll('.sec');
    expect(secs).toHaveLength(layout.sections.filter((s) => s.area).length);
    layout.sections.forEach((s, i) => {
      expect(css(secs[i]!).gridRow).toBe(rows(s.area!));
      expect(css(secs[i]!).gridColumn).toBe(`${s.area!.column + 1} / span ${s.area!.w}`);
    });
    // Inside a block, tiles keep their section grid.
    expect(w.findAll('.sec-grid .cell').length).toBe(board.sections.flatMap((s) => s.items).length);
  });

  it('places every Mosaic tile and label on the board, in a region per section', async () => {
    const board = setFlow(start(), 'mosaic');
    const h0 = harness(docs(withIncidents));
    const Page = defineComponent({ setup: () => () => h('main', { 'aria-label': 'Front page' }, [h(StaticBoard, { board, breakpoint: 'desktop' })]) });
    const w = h0.mountIn(Page);
    const layout = deriveLayout(board, 'desktop', { headers: 'always' });
    expect(w.find('.sec-grid').exists()).toBe(false);
    for (const s of layout.sections) {
      const region = w.find(`section[aria-label="${s.title ?? s.id}"]`);
      expect(region.classes()).toContain('mosaic-sec');
      expect(css(region.find('.mosaic-label')).gridRow).toBe(rows(s.header!));
      for (const i of s.items) expect(css(w.find(`.cell[data-item="${i.id}"]`)).gridRow).toBe(rows(i.area!));
    }
    expect(await axeViolations(w.element)).toEqual([]);
  });

  it('fits the phone in every flow', () => {
    for (const flow of ['bento', 'mosaic'] as const) {
      const w = harness(docs(withIncidents)).mountIn(StaticBoard, { board: setFlow(start(), flow), breakpoint: 'phone' });
      expect(css(w.find('.board')).getPropertyValue('--cols')).toBe('4');
      const cells = w.findAll('.cell').map((c) => css(c).gridColumn);
      expect(cells.every((c) => Number(c.split(' / span ')[0]) + Number(c.split(' / span ')[1]) - 1 <= 4)).toBe(true);
    }
  });
});

/** A board store that keeps what it saves. */
function fakeStore(initial: Board) {
  const board = shallowRef<Board | undefined>(initial);
  const store = {
    state: reactive({ unsaved: false, boardId: 'b1', status: 'ready' }),
    board,
    tiles: shallowRef({}),
    load: vi.fn(),
    save: vi.fn(async (b: Board) => {
      board.value = b;
      return true;
    }),
  };
  return store as unknown as BoardStore & { save: typeof store.save };
}

describe('choosing the flow in edit mode', () => {
  it('saves the choice, undoes it, and shows a mosaic as bento while editing', async () => {
    const store = fakeStore(start());
    const hs = harness(docs(withIncidents));
    const Page = defineComponent({
      setup() {
        provideBoard(store);
        return () => h(BoardSurface, { board: store.board.value!, breakpoint: 'desktop' });
      },
    });
    const w = hs.mountIn(Page);
    hs.ui.state.editing = true;
    await vi.dynamicImportSettled();
    await flushPromises();

    const group = w.find('.flow-seg');
    expect(group.attributes()).toMatchObject({ role: 'group', 'aria-label': 'Layout' });
    const pick = (name: string) => group.findAll('button').find((b) => b.text() === name)!;
    expect(pick('Sections').attributes('aria-pressed')).toBe('true');

    await pick('Bento').trigger('click');
    await flushPromises();
    expect(store.save).toHaveBeenLastCalledWith(expect.objectContaining({ flow: 'bento' }));
    expect(pick('Bento').attributes('aria-pressed')).toBe('true');
    expect(w.find('.layout-grid').attributes('data-flow')).toBe('bento');

    await pick('Mosaic').trigger('click');
    await flushPromises();
    expect(store.board.value!.flow).toBe('mosaic');
    expect(w.findComponent(LayoutBoard).find('.layout-grid').attributes('data-flow')).toBe('bento');

    await w.findAll('button').find((b) => b.text() === 'Undo')!.trigger('click');
    await flushPromises();
    expect(store.board.value!.flow).toBe('bento');

    await w.findAll('button').find((b) => b.text() === 'Done')!.trigger('click');
    await flushPromises();
    expect(w.find('.board-flow > .board').attributes('data-flow')).toBe('bento');
  });
});
