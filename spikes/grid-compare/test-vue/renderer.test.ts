/**
 * The renderers side by side on the dogfood front page, in happy-dom:
 * our <LayoutBoard> against gridstack 14's official Vue wrapper.
 * Keyboard, ARIA, reduced motion, and how each one holds the layout.
 */
import { createBoard, deriveLayout, type Board, type BoardInput } from '@chadash/layout';
import { LayoutBoard } from '@chadash/layout-vue';
import { mount } from '@vue/test-utils';
import { GridStack as GridStackVue } from 'gridstack/dist/vue';
import type { GridStackOptions } from 'gridstack';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { defineComponent, h, nextTick } from 'vue';
// happy-dom has no layout engine; layout-vue's tests describe one (100 px pitch)
import { stubGeometry } from '../../../packages/layout-vue/test/helpers.ts';
import { DOGFOOD, SIZE_UNITS } from '../scenarios/index.ts';
import { constraints } from '../adapters/gridstack.ts';

const NAMES: Record<string, string> = { fleet: 'Fleet', plex: 'Now playing', opnsense: 'OPNsense', backups: 'Backups' };
const label = (item: { id: string }) => NAMES[item.id] ?? item.id;
const dogfood = (): Board => createBoard(DOGFOOD as unknown as BoardInput);
const key = (el: Element, k: string, init: KeyboardEventInit = {}) => el.dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true, ...init }));
const tick = async () => {
  await nextTick();
  await nextTick();
};
const placed = (board: Board, id: string) => deriveLayout(board, 'desktop').sections.flatMap((s) => s.items).find((i) => i.id === id)!;

let restore: () => void;
beforeEach(() => {
  restore = stubGeometry();
});
afterEach(() => {
  restore();
  document.body.innerHTML = '';
  document.documentElement.removeAttribute('data-motion');
  vi.unstubAllGlobals();
});

function mountOurs(props: Record<string, unknown> = {}) {
  const commits: Board[] = [];
  const wrapper = mount(LayoutBoard, {
    attachTo: document.body,
    props: {
      board: dogfood(),
      editing: true,
      breakpoint: 'desktop',
      motion: 'none',
      itemLabel: label,
      ...props,
      'onUpdate:board': (b: Board) => void wrapper.setProps({ board: b }),
      onCommit: (b: Board) => void commits.push(b),
    },
    slots: { item: (s: { item: { id: string } }) => h('div', { class: 'widget' }, label(s.item)) },
  });
  const root = wrapper.element as HTMLElement;
  const item = (id: string) => root.querySelector<HTMLElement>(`[data-item-id="${id}"]`)!;
  const status = () => root.querySelector('[role="status"]')?.textContent?.trim() ?? '';
  return { wrapper, root, commits, item, status };
}

function gridstackOptions(animate: boolean): GridStackOptions {
  return {
    column: 12,
    cellHeight: 80,
    animate,
    mode: 'list',
    subGridOpts: { column: 'auto', mode: 'compact', animate, acceptWidgets: true },
    children: DOGFOOD.sections.map((s) => ({
      id: s.id,
      w: s.span,
      h: 6,
      subGridOpts: { children: s.items.map((i) => ({ id: i.id, ...SIZE_UNITS[i.size], ...constraints(i.supportedSizes), component: 'Tile', props: { name: label(i) } })) },
    })),
  };
}

function mountGridstack(animate = false) {
  const Tile = defineComponent({ props: { name: { type: String, default: '' } }, setup: (p) => () => h('div', { class: 'widget' }, p.name) });
  const wrapper = mount(GridStackVue, { attachTo: document.body, props: { options: gridstackOptions(animate), components: { Tile } } });
  const item = (id: string) => document.querySelector<HTMLElement>(`.grid-stack-item[gs-id="${id}"]`)!;
  return { wrapper, item };
}

describe('@chadash/layout-vue', () => {
  it('names the board and every widget for assistive tech', async () => {
    const { root: board, item } = mountOurs();
    await tick();
    expect(board.getAttribute('role')).toBe('group');
    expect(board.getAttribute('aria-label')).toBe('Board layout');
    expect(item('fleet').getAttribute('role')).toBe('button');
    expect(item('fleet').getAttribute('aria-roledescription')).toBe('movable widget');
    expect(item('fleet').getAttribute('aria-label')).toBe('Fleet, Large');
    expect(item('fleet').getAttribute('aria-describedby')).toBeTruthy();
    expect(board.querySelector('[role="status"]')?.getAttribute('aria-live')).toBe('polite');
    const stops = [...board.querySelectorAll<HTMLElement>('[data-item-id]')].map((el) => el.tabIndex);
    expect(stops.filter((t) => t === 0)).toHaveLength(1); // roving tabindex: one tab stop for 15 widgets
  });

  it('moves the Large tile across its section with Enter, → and Enter, and says so', async () => {
    const { item, commits, status } = mountOurs();
    await tick();
    item('fleet').focus();
    key(item('fleet'), 'Enter');
    await tick();
    expect(status()).toContain('Picked up Fleet');
    key(item('fleet'), 'ArrowRight');
    await tick();
    key(item('fleet'), 'Enter');
    await tick();
    expect(commits).toHaveLength(1);
    expect(placed(commits[0]!, 'fleet')).toMatchObject({ x: 4, y: 0 });
    expect(status()).toContain('Dropped Fleet');
  });

  it('resizes with Shift+arrows, only through declared sizes', async () => {
    const { item, commits, status } = mountOurs();
    await tick();
    item('plex').focus();
    key(item('plex'), 'ArrowDown', { shiftKey: true }); // Wide → Large (declared)
    await tick();
    expect(placed(commits.at(-1)!, 'plex').size).toBe('large');
    key(item('plex'), 'ArrowDown', { shiftKey: true }); // no taller size declared
    await tick();
    expect(commits).toHaveLength(1);
    expect(status()).toContain("can't get any taller");
  });

  it('crossfades instead of moving under reduced motion', async () => {
    document.documentElement.setAttribute('data-motion', 'reduced');
    const { item } = mountOurs({ motion: 'auto' });
    await tick();
    item('fleet').focus();
    key(item('fleet'), 'Enter');
    key(item('fleet'), 'ArrowRight'); // opnsense reflows left
    await tick();
    expect(item('opnsense').style.transform).not.toMatch(/translate3d\(-?[1-9]/);
    expect(Number(item('opnsense').style.opacity)).toBeLessThan(1);
  });

  it('springs neighbours from their old slot (FLIP on Motion springs) at full motion', async () => {
    const { item } = mountOurs({ motion: 'full' });
    await tick();
    item('fleet').focus();
    key(item('fleet'), 'Enter');
    key(item('fleet'), 'ArrowRight');
    await tick();
    // opnsense moved 4 cells left; it starts 400 px to the right of its new slot and springs home
    expect(item('opnsense').style.transform).toMatch(/^translate3d\(400px, 0px, 0\)/);
  });
});

describe('gridstack 14 Vue wrapper (gridstack/dist/vue)', () => {
  it('renders the dogfood page through <GridStack>, sections as sub-grids', async () => {
    const { item } = mountGridstack();
    await tick();
    for (const id of ['cameras', 'fleet', 'plex', 'incidents']) expect(item(id)).toBeTruthy();
    expect(document.querySelectorAll('.grid-stack-item .grid-stack-item').length).toBe(15);
  });

  it('exposes no roles, labels, tab stops or live region', async () => {
    const { item } = mountGridstack();
    await tick();
    for (const attr of ['role', 'tabindex', 'aria-label', 'aria-roledescription', 'aria-describedby']) expect(item('fleet').getAttribute(attr)).toBeNull();
    expect(document.querySelector('[role="status"], [aria-live]')).toBeNull();
  });

  it('ignores Enter and arrows', async () => {
    const { item } = mountGridstack();
    await tick();
    const before = item('fleet').getAttribute('gs-x');
    item('fleet').focus();
    for (const k of ['Enter', 'ArrowRight', ' ']) key(item('fleet'), k);
    key(item('opnsense'), 'ArrowDown', { shiftKey: true });
    await tick();
    expect(item('fleet').getAttribute('gs-x')).toBe(before);
    expect(item('opnsense').getAttribute('gs-h')).toBe('2');
  });

  it('keeps the same 0.3 s CSS transition class under reduced motion', async () => {
    vi.stubGlobal('matchMedia', (q: string) => ({ matches: q.includes('reduce'), media: q, addEventListener() {}, removeEventListener() {} }));
    mountGridstack(true);
    await tick();
    expect(document.querySelector('.grid-stack')!.classList.contains('grid-stack-animate')).toBe(true);
  });
});
