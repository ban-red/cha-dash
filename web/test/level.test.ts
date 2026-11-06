/**
 * Levels of detail (lib/level.ts): the surface provides one, every tile is
 * marked with it, the phone breakpoint is compact, and at ink a time is
 * absolute.
 */
import { afterEach, describe, expect, it } from 'vitest';
import { flushPromises, mount } from '@vue/test-utils';
import { defineComponent, h } from 'vue';
import StaticBoard from '../src/board/StaticBoard.vue';
import WidgetHost from '../src/widgets/WidgetHost.vue';
import { ago, setDisplayTimeZone } from '../src/lib/format';
import { currentLevel, provideLevel, useLevel, type Level } from '../src/lib/level';
import { defaultBoard } from '../src/model/defaultLayout';
import { docs, withIncidents } from './fixtures';
import { harness } from './harness';

const NOW = Date.parse(withIncidents.now);

afterEach(() => {
  document.body.innerHTML = '';
});

/** Renders what ago() says, and the level, under a provided level. */
function agoAt(level: Level | undefined) {
  const Child = defineComponent({
    setup() {
      const l = useLevel();
      return () => h('p', `${l.value}|${currentLevel()}|${ago(NOW - 4 * 60_000, NOW)}`);
    },
  });
  const Parent = defineComponent({
    setup() {
      if (level) provideLevel(level);
      return () => h(Child);
    },
  });
  return mount(Parent).text();
}

describe('the level', () => {
  it('is full where nothing provides one', () => {
    setDisplayTimeZone('UTC');
    expect(agoAt(undefined)).toBe('full|full|4m ago');
    expect(currentLevel()).toBe('full'); // outside any component
  });
  it('makes times absolute at ink, and only there', () => {
    setDisplayTimeZone('UTC');
    expect(agoAt('ink')).toBe('ink|ink|at 14:01');
    expect(agoAt('simplified')).toBe('simplified|simplified|4m ago');
    expect(agoAt('compact')).toBe('compact|compact|4m ago');
  });
});

describe('the board gives its tiles a level', () => {
  const board = defaultBoard(docs(withIncidents).graph, NOW);
  const Board = defineComponent({
    props: { breakpoint: { type: String, default: undefined }, level: { type: String, default: undefined } },
    setup: (p) => () =>
      h(StaticBoard, { board, breakpoint: p.breakpoint as 'desktop' | 'phone' | undefined, level: p.level as Level | undefined }, {
        item: ({ item, size }: { item: { id: string; type: string }; size: string }) => h(WidgetHost, { item, size: size as never }),
      }),
  });
  for (const [bp, level, want] of [
    ['desktop', undefined, 'full'],
    ['phone', undefined, 'compact'],
    ['desktop', 'simplified', 'simplified'],
    ['phone', 'ink', 'ink'],
  ] as const) {
    it(`${bp}${level ? ` with ${level}` : ''}: ${want}`, async () => {
      const hs = harness(docs(withIncidents));
      const w = hs.mountIn(Board, { breakpoint: bp, level });
      await flushPromises();
      const tiles = w.findAll('.tile');
      expect(tiles.length).toBeGreaterThan(3);
      for (const t of tiles) {
        expect(t.classes()).toContain(`lvl-${want}`);
        expect(t.attributes('data-level')).toBe(want);
      }
      if (want === 'simplified' || want === 'ink') expect(w.find('.tile-cog').exists()).toBe(false);
      w.unmount();
      hs.live.stop();
    });
  }
});
