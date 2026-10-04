import { checkInvariants, createBoard, type Board, type Breakpoint } from '@chadash/layout';
import { mount, type VueWrapper } from '@vue/test-utils';
import { h, toRaw } from 'vue';
import LayoutBoard from '../src/LayoutBoard.vue';
import type { LayoutBoardProps } from '../src/types.ts';

/** A small home: two sections side by side, then a full-width one. */
export function homeBoard(): Board {
  return createBoard({
    sections: [
      {
        id: 'infra',
        title: 'Infrastructure',
        span: 8,
        items: [
          { id: 'pve', type: 'proxmox.node', size: 'large', supportedSizes: ['wide', 'large'] },
          { id: 'nas', type: 'unraid.array', size: 'wide', supportedSizes: ['small', 'wide', 'large'] },
          { id: 'dns', type: 'pihole', size: 'small', supportedSizes: ['small', 'wide'] },
          { id: 'plex', type: 'plex', size: 'small', supportedSizes: ['small', 'wide', 'large'] },
        ],
      },
      {
        id: 'side',
        title: 'Today',
        span: 4,
        items: [
          { id: 'clock', type: 'clock', size: 'small', supportedSizes: ['small', 'wide'] },
          { id: 'weather', type: 'weather', size: 'wide', supportedSizes: ['small', 'wide'] },
        ],
      },
      {
        id: 'media',
        title: 'Media',
        span: 12,
        items: [
          { id: 'cams', type: 'frigate', size: 'hero', supportedSizes: ['large', 'hero'] },
          { id: 'sonarr', type: 'sonarr', size: 'small', supportedSizes: ['small', 'wide'] },
        ],
      },
    ],
  });
}

/** One full-width section, for drag tests where geometry is simple. */
export function rowBoard(): Board {
  return createBoard({
    sections: [
      {
        id: 'main',
        title: 'Main',
        span: 12,
        items: [
          { id: 'plex', type: 'plex', size: 'small', supportedSizes: ['small', 'wide', 'large'] },
          { id: 'nas', type: 'unraid.array', size: 'wide', supportedSizes: ['small', 'wide', 'large'] },
          { id: 'dns', type: 'pihole', size: 'small', supportedSizes: ['small', 'wide'] },
        ],
      },
    ],
  });
}

const NAMES: Record<string, string> = {
  pve: 'Proxmox',
  nas: 'NAS',
  dns: 'Pi-hole',
  plex: 'Plex',
  clock: 'Clock',
  weather: 'Weather',
  cams: 'Cameras',
  sonarr: 'Sonarr',
};

export interface Harness {
  wrapper: VueWrapper;
  /** Every `update:board` payload, in order. */
  updates: Board[];
  /** Every `commit` payload, in order. */
  commits: Board[];
  selects: string[];
  /** The board prop as the parent currently passes it (raw). */
  board(): Board;
  item(id: string): HTMLElement;
  announcement(): string;
}

/**
 * Mount a board. By default the harness behaves like `v-model:board`: every
 * `update:board` is fed back as the `board` prop.
 */
export function mountBoard(props: Partial<LayoutBoardProps> & { board: Board }, options: { vModel?: boolean } = {}): Harness {
  const updates: Board[] = [];
  const commits: Board[] = [];
  const selects: string[] = [];
  const vModel = options.vModel ?? true;
  const wrapper: VueWrapper = mount(LayoutBoard, {
    attachTo: document.body,
    props: {
      motion: 'none',
      itemLabel: (item: { id: string }) => NAMES[item.id] ?? item.id,
      ...props,
      'onUpdate:board': (board: Board) => {
        updates.push(board);
        if (vModel) void wrapper.setProps({ board });
      },
      onCommit: (board: Board) => {
        commits.push(board);
      },
      onSelect: (id: string) => {
        selects.push(id);
      },
    },
    slots: {
      item: (slot: { item: { id: string }; size: string; dragging: boolean }) =>
        h('div', { class: 'widget', 'data-widget': slot.item.id, 'data-dragging': String(slot.dragging) }, `${slot.item.id}:${slot.size}`),
    },
  }) as VueWrapper;
  return {
    wrapper,
    updates,
    commits,
    selects,
    board() {
      return toRaw((wrapper.vm.$props as { board: Board }).board);
    },
    item(id) {
      const el = (wrapper.element as HTMLElement).querySelector<HTMLElement>(`[data-item-id="${id}"]`);
      if (el === null) throw new Error(`no item ${id}`);
      return el;
    },
    announcement() {
      return (wrapper.element as HTMLElement).querySelector('[role="status"]')?.textContent?.trim() ?? '';
    },
  };
}

export function expectValid(boards: readonly Board[]): void {
  for (const board of boards) {
    const problems = checkInvariants(board);
    if (problems.length > 0) throw new Error(`invalid board: ${problems.join('; ')}`);
  }
}

export function key(el: Element, key: string, init: KeyboardEventInit = {}): void {
  el.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true, ...init }));
}

// ---------------------------------------------------------------------------
// Geometry: happy-dom has no layout, so tests describe one.
// ---------------------------------------------------------------------------

/** Cell pitch (unit + gutter) and gutter of the fake layout, px. */
export const PITCH = 100;
export const GAP = 12;
const BAND_HEIGHT = 1000;

function num(el: Element, name: string): number {
  return Number((el as HTMLElement).dataset[name] ?? 0);
}

/** Section areas and items get boxes from their data attributes: column × PITCH, band × 1000 + row × PITCH. */
function boxOf(el: Element): { left: number; top: number; width: number; height: number } | undefined {
  const ds = (el as HTMLElement).dataset;
  if (ds === undefined) return undefined;
  if (ds['sectionArea'] !== undefined) {
    return {
      left: num(el, 'column') * PITCH,
      top: num(el, 'band') * BAND_HEIGHT,
      width: num(el, 'span') * PITCH - GAP,
      height: Math.max(1, num(el, 'rows')) * PITCH - GAP,
    };
  }
  if (ds['itemId'] !== undefined) {
    return {
      left: num(el, 'column') * PITCH,
      top: num(el, 'band') * BAND_HEIGHT + num(el, 'y') * PITCH,
      width: num(el, 'w') * PITCH - GAP,
      height: num(el, 'h') * PITCH - GAP,
    };
  }
  return undefined;
}

/** Install the fake layout; returns a function that removes it. */
export function stubGeometry(): () => void {
  const proto = HTMLElement.prototype;
  const saved = {
    rect: Object.getOwnPropertyDescriptor(Element.prototype, 'getBoundingClientRect'),
    left: Object.getOwnPropertyDescriptor(proto, 'offsetLeft'),
    top: Object.getOwnPropertyDescriptor(proto, 'offsetTop'),
    width: Object.getOwnPropertyDescriptor(proto, 'offsetWidth'),
    height: Object.getOwnPropertyDescriptor(proto, 'offsetHeight'),
    style: window.getComputedStyle,
  };
  const zero = { left: 0, top: 0, width: 0, height: 0 };
  Object.defineProperty(Element.prototype, 'getBoundingClientRect', {
    configurable: true,
    value(this: Element) {
      const b = boxOf(this) ?? (this.classList.contains('layout-grid') ? { left: 0, top: 0, width: 12 * PITCH - GAP, height: 0 } : zero);
      return { ...b, x: b.left, y: b.top, right: b.left + b.width, bottom: b.top + b.height, toJSON: () => b };
    },
  });
  for (const [prop, pick] of [
    ['offsetLeft', 'left'],
    ['offsetTop', 'top'],
    ['offsetWidth', 'width'],
    ['offsetHeight', 'height'],
  ] as const) {
    Object.defineProperty(proto, prop, {
      configurable: true,
      get(this: HTMLElement) {
        return (boxOf(this) ?? zero)[pick];
      },
    });
  }
  window.getComputedStyle = ((el: Element, pseudo?: string | null) => {
    const style = saved.style.call(window, el, pseudo);
    if (!(el as HTMLElement).classList?.contains('layout-grid')) return style;
    return new Proxy(style, {
      get(target, prop) {
        if (prop === 'columnGap') return `${GAP}px`;
        const value = Reflect.get(target, prop, target) as unknown;
        return typeof value === 'function' ? (value as (...args: unknown[]) => unknown).bind(target) : value;
      },
    });
  }) as typeof window.getComputedStyle;
  return () => {
    if (saved.rect) Object.defineProperty(Element.prototype, 'getBoundingClientRect', saved.rect);
    for (const [prop, desc] of [
      ['offsetLeft', saved.left],
      ['offsetTop', saved.top],
      ['offsetWidth', saved.width],
      ['offsetHeight', saved.height],
    ] as const) {
      if (desc) Object.defineProperty(proto, prop, desc);
    }
    window.getComputedStyle = saved.style;
  };
}

// ---------------------------------------------------------------------------
// Pointer events with explicit timestamps
// ---------------------------------------------------------------------------

export function pointer(target: EventTarget, type: string, x: number, y: number, t: number, init: PointerEventInit = {}): PointerEvent {
  const event = new PointerEvent(type, {
    bubbles: true,
    cancelable: true,
    clientX: x,
    clientY: y,
    pointerId: 1,
    pointerType: 'mouse',
    isPrimary: true,
    button: 0,
    ...init,
  });
  Object.defineProperty(event, 'timeStamp', { value: t });
  target.dispatchEvent(event);
  return event;
}

/**
 * Drag from (x0, y0) to (x1, y1) in `steps` moves spread over `duration` ms,
 * then release (after `rest` ms without moving).
 */
export function drag(
  el: Element,
  from: { x: number; y: number },
  to: { x: number; y: number },
  options: { duration: number; steps?: number; rest?: number; start?: number },
): void {
  const steps = options.steps ?? 20;
  const t0 = options.start ?? 1000;
  pointer(el, 'pointerdown', from.x, from.y, t0);
  for (let i = 1; i <= steps; i++) {
    const f = i / steps;
    pointer(window, 'pointermove', from.x + (to.x - from.x) * f, from.y + (to.y - from.y) * f, t0 + options.duration * f);
  }
  pointer(window, 'pointerup', to.x, to.y, t0 + options.duration + (options.rest ?? 0));
}

export const BREAKPOINT_LIST: readonly Breakpoint[] = ['desktop', 'tablet', 'phone'];
