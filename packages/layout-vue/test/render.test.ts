import { deriveLayout } from '@chadash/layout';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { nextTick } from 'vue';
import { BREAKPOINT_LIST, homeBoard, mountBoard } from './helpers.ts';

afterEach(() => {
  document.body.innerHTML = '';
  vi.unstubAllGlobals();
});

describe('rendered positions', () => {
  for (const breakpoint of BREAKPOINT_LIST) {
    it(`match deriveLayout at ${breakpoint}`, () => {
      const board = homeBoard();
      const { wrapper, item } = mountBoard({ board, breakpoint });
      const layout = deriveLayout(board, breakpoint);
      const root = wrapper.element as HTMLElement;
      expect(root.dataset['breakpoint']).toBe(breakpoint);
      expect((root.querySelector('.layout-grid') as HTMLElement).style.getPropertyValue('--layout-columns')).toBe(String(layout.columns));

      const rendered = [...root.querySelectorAll<HTMLElement>('[data-item-id]')].map((el) => el.dataset['itemId']);
      expect(rendered).toEqual(layout.sections.flatMap((s) => s.items.map((it) => it.id)));

      for (const section of layout.sections) {
        for (const placed of section.items) {
          const el = item(placed.id);
          expect(el.dataset['section']).toBe(section.id);
          expect(Number(el.dataset['x'])).toBe(placed.x);
          expect(Number(el.dataset['y'])).toBe(placed.y);
          expect(Number(el.dataset['w'])).toBe(placed.w);
          expect(Number(el.dataset['h'])).toBe(placed.h);
          expect(el.dataset['size']).toBe(placed.size);
          // Board column = section column + x; the CSS grid lines say the same.
          expect(Number(el.dataset['column'])).toBe(section.column + placed.x);
          expect(el.style.gridColumn.replace(/\s+/g, ' ')).toBe(`${section.column + placed.x + 1} / span ${placed.w}`);
          expect(el.style.gridRow).toMatch(new RegExp(`^\\d+ / span ${placed.h}$`));
          expect(el.querySelector('[data-widget]')?.textContent).toBe(`${placed.id}:${placed.size}`);
        }
      }
    });
  }

  it('lays bands out as header, unit rows, spacer', () => {
    const board = homeBoard();
    const { wrapper, item } = mountBoard({ board, breakpoint: 'desktop' });
    const grid = wrapper.element.querySelector('.layout-grid') as HTMLElement;
    // Band 0: header + 4 unit rows (pve is 4 tall), spacer; band 1: header + 4 rows (cams).
    expect(grid.style.gridTemplateRows).toBe(
      'auto repeat(4, var(--layout-unit)) var(--layout-band-gap) auto repeat(4, var(--layout-unit))',
    );
    expect(item('pve').style.gridRow).toBe('2 / span 4');
    expect(item('dns').style.gridRow).toBe('4 / span 2');
    expect(item('cams').style.gridRow).toBe('8 / span 4');
    const headers = [...wrapper.element.querySelectorAll('.layout-section-header')].map((el) => el.textContent?.trim());
    expect(headers).toEqual(['Infrastructure', 'Today', 'Media']);
  });

  it('derives the breakpoint from the container width when none is given', async () => {
    const observers: { cb: ResizeObserverCallback; el?: Element }[] = [];
    class FakeResizeObserver {
      readonly #entry: { cb: ResizeObserverCallback; el?: Element };
      constructor(cb: ResizeObserverCallback) {
        this.#entry = { cb };
        observers.push(this.#entry);
      }
      observe(el: Element): void {
        this.#entry.el = el;
      }
      unobserve(): void {}
      disconnect(): void {}
    }
    vi.stubGlobal('ResizeObserver', FakeResizeObserver);
    const board = homeBoard();
    const { wrapper, item } = mountBoard({ board });
    await nextTick();
    const resizeTo = async (width: number): Promise<void> => {
      for (const o of observers) {
        o.cb([{ contentRect: { width }, contentBoxSize: [{ inlineSize: width, blockSize: 0 }], target: o.el } as unknown as ResizeObserverEntry], {} as ResizeObserver);
      }
      await nextTick();
    };
    const root = wrapper.element as HTMLElement;
    for (const [width, breakpoint] of [
      [1200, 'desktop'],
      [800, 'tablet'],
      [400, 'phone'],
    ] as const) {
      await resizeTo(width);
      expect(root.dataset['breakpoint']).toBe(breakpoint);
      const layout = deriveLayout(board, breakpoint);
      for (const section of layout.sections) {
        for (const placed of section.items) {
          expect(Number(item(placed.id).dataset['column'])).toBe(section.column + placed.x);
          expect(item(placed.id).dataset['size']).toBe(placed.size);
        }
      }
    }
  });

  it('renders no widget content of its own, only the slot', () => {
    const { item } = mountBoard({ board: homeBoard(), breakpoint: 'desktop' });
    const content = item('nas').querySelector('.layout-item-content') as HTMLElement;
    expect(content.children).toHaveLength(1);
    expect(content.firstElementChild?.getAttribute('data-widget')).toBe('nas');
  });
});
