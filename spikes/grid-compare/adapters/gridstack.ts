/**
 * gridstack 14 behind the adapter contract, driven through its public DOM API
 * (`GridStack`), the way an app would. Needs a DOM: happy-dom in tests, the
 * browser on the side-by-side page.
 *
 * Mapping our model onto gridstack:
 * - **Sections** are nested sub-grids (`subGridOpts`, `column: 'auto'`), the
 *   only section concept gridstack has. Section widgets sit on a 12-column
 *   parent grid with `w` = span.
 * - **Supported sizes** become `minW/maxW/minH/maxH`, the only size
 *   constraints gridstack has. Programmatic resizes pass exact sizes.
 * - **Undo** is app code: a stack of `save()` snapshots, restored with `load()`.
 *
 * Two things a browser does that happy-dom can't, emulated with gridstack's own
 * code paths (see `relayoutSections`):
 * - a ResizeObserver tick calls `onResize()` on each sub-grid, which makes it
 *   follow its parent widget's width (`column: 'auto'`);
 * - `sizeToContent` measures the sub-grid and grows the section widget to
 *   `subGrid.getRow()` rows. We set `h` to that directly.
 */
import { GridStack, type ColumnOptions, type GridItemHTMLElement, type GridStackMode, type GridStackNode, type GridStackOptions, type GridStackWidget } from 'gridstack';
import { SIZE_UNITS, type BoardData, type Columns, type SizeName, type Target, type WidgetData } from '../scenarios/types.ts';
import type { GridAdapter, PlacedSection, Snapshot } from '../harness/types.ts';

export interface GridstackConfig {
  /** Packing inside sections: 'top' is gridstack's default (top gravity), 'compact' is v14's dense row-major reflow. */
  readonly mode: GridStackMode;
  /** Packing of the sections on the board; defaults to `mode`. 'list' flows sections in order without back-filling. */
  readonly sectionMode?: GridStackMode;
  /** Re-layout on column change; undefined = gridstack's default for the mode ('moveScale', or the mode itself for list/compact). */
  readonly layout?: ColumnOptions;
}

/** min/max box around a widget's supported sizes: the closest gridstack gets to "supported sizes only". */
export function constraints(supported: readonly SizeName[]) {
  const ws = supported.map((s) => SIZE_UNITS[s].w);
  const hs = supported.map((s) => SIZE_UNITS[s].h);
  return { minW: Math.min(...ws), maxW: Math.max(...ws), minH: Math.min(...hs), maxH: Math.max(...hs) };
}

function widget(item: WidgetData, at?: { x: number; y: number }): GridStackWidget {
  const { w, h } = SIZE_UNITS[item.size];
  return { id: item.id, w, h, ...constraints(item.supportedSizes), ...(at ?? {}) };
}

let widthTick = 1;

export class GridstackAdapter implements GridAdapter {
  readonly name: string;
  private root: HTMLElement | null = null;
  private grid: GridStack | null = null;
  private past: GridStackWidget[][] = [];
  private host: HTMLElement;

  constructor(
    readonly config: GridstackConfig = { mode: 'top' },
    host?: HTMLElement,
  ) {
    const extras = [config.sectionMode && `sections ${config.sectionMode}`, config.layout && `${String(config.layout)} columns`].filter(Boolean);
    this.name = `gridstack 14 (${[config.mode, ...extras].join(', ')})`;
    this.host = host ?? document.body;
  }

  private options(children: GridStackWidget[]): GridStackOptions {
    const { mode, layout, sectionMode = mode } = this.config;
    return {
      column: 12,
      cellHeight: 80,
      margin: 6,
      animate: false,
      mode: sectionMode,
      ...(layout ? { layout } : {}),
      subGridOpts: { column: 'auto', cellHeight: 80, margin: 6, animate: false, acceptWidgets: true, mode, ...(layout ? { layout } : {}) },
      children,
    };
  }

  private init(opts: GridStackOptions): void {
    this.grid?.destroy(true);
    this.root?.remove();
    this.root = document.createElement('div');
    this.root.className = 'grid-stack';
    this.host.appendChild(this.root);
    this.grid = GridStack.init(opts, this.root);
    this.relayoutSections();
  }

  private get g(): GridStack {
    if (!this.grid) throw new Error('not loaded');
    return this.grid;
  }

  /** Section widgets on the parent grid, in reading order. */
  private sections(): GridStackNode[] {
    return [...this.g.engine.nodes].sort((a, b) => (a.y ?? 0) - (b.y ?? 0) || (a.x ?? 0) - (b.x ?? 0));
  }

  private section(id: string): GridStack {
    const n = this.g.engine.nodes.find((s) => s.id === id);
    if (!n?.subGrid) throw new Error(`no section ${id}`);
    return n.subGrid;
  }

  private find(id: string): { sub: GridStack; node: GridStackNode; el: GridItemHTMLElement } {
    for (const s of this.g.engine.nodes) {
      const node = s.subGrid?.engine.nodes.find((n) => n.id === id);
      if (s.subGrid && node?.el) return { sub: s.subGrid, node, el: node.el };
    }
    throw new Error(`no widget ${id}`);
  }

  /** What the browser would do after a change: sub-grids follow their widget's width, sections fit their content. */
  private relayoutSections(): void {
    // copies: update() re-sorts engine.nodes in place
    for (const s of [...this.g.engine.nodes]) {
      // gridstack's own path: onResize() on an auto-column sub-grid calls column(parent w, 'list')
      s.subGrid?.onResize(1000 + widthTick++, 1);
    }
    for (const s of [...this.g.engine.nodes]) {
      const rows = Math.max(1, s.subGrid?.getRow() ?? 1);
      if (s.el && s.h !== rows) this.g.update(s.el, { h: rows });
    }
  }

  /** Run an edit and keep an undo snapshot, unless the edit changed nothing (like a drop back in place). */
  private edit(fn: () => void): void {
    const before = this.saved();
    fn();
    this.relayoutSections();
    if (JSON.stringify(this.saved()) !== JSON.stringify(before)) this.past.push(before);
  }

  /** The layout at the current column count (by default save() writes the widest cached layout). */
  private saved(): GridStackWidget[] {
    return this.g.save(false, false, undefined, this.g.getColumn()) as GridStackWidget[];
  }

  load(board: BoardData): void {
    this.past = [];
    this.init(
      this.options(
        board.sections.map((s) => ({
          id: s.id,
          w: s.span,
          h: 1,
          sizeToContent: true,
          subGridOpts: { children: s.items.map((i) => widget(i)) },
        })),
      ),
    );
  }

  move(item: string, to: Target): void {
    this.edit(() => {
      const { sub, node, el } = this.find(item);
      const dest = this.section(to.section);
      if (dest === sub) {
        sub.update(el, { x: to.x, y: to.y });
      } else {
        // cross-grid: what a drop with acceptWidgets ends up doing, minus the DOM node reuse
        const { id, w, h, minW, maxW, minH, maxH } = node;
        sub.removeWidget(el, true, false);
        dest.addWidget({ id, w, h, minW, maxW, minH, maxH, x: to.x, y: to.y } as GridStackWidget);
      }
    });
  }

  resize(item: string, size: SizeName): void {
    this.edit(() => {
      const { sub, el } = this.find(item);
      sub.update(el, { ...SIZE_UNITS[size] });
    });
  }

  insert(item: WidgetData, near?: Target): void {
    this.edit(() => {
      if (near) {
        this.section(near.section).addWidget(widget(item, { x: near.x, y: near.y }));
      } else {
        // gridstack has no board-level placement: the app picks a section (here the first wide enough)
        const s = this.sections().find((n) => (n.subGrid?.getColumn() ?? 0) >= SIZE_UNITS[item.size].w);
        s?.subGrid?.addWidget(widget(item));
      }
    });
  }

  setColumns(columns: Columns): void {
    this.g.column(columns, this.config.layout);
    this.relayoutSections();
  }

  undo(): void {
    const prev = this.past.pop();
    if (prev) {
      this.g.load(prev);
      this.relayoutSections();
    }
  }

  serialize(): string {
    return JSON.stringify(this.g.save(false, true));
  }

  restore(json: string): void {
    this.past = [];
    this.init(JSON.parse(json) as GridStackOptions);
  }

  snapshot(): Snapshot {
    const sections: PlacedSection[] = this.sections().map((s) => ({
      id: String(s.id),
      x: s.x ?? 0,
      y: s.y ?? 0,
      columns: s.w ?? 1,
      rows: s.h ?? 1,
      items: (s.subGrid?.engine.nodes ?? []).map((n) => ({ id: String(n.id), x: n.x ?? 0, y: n.y ?? 0, w: n.w ?? 1, h: n.h ?? 1 })),
    }));
    return { columns: this.g.getColumn() as Columns, sections };
  }

  dispose(): void {
    this.grid?.destroy(true);
    this.grid = null;
    this.root?.remove();
    this.root = null;
  }
}
