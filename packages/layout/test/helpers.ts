import fc from 'fast-check';
import {
  BREAKPOINT_COLUMNS,
  BREAKPOINTS,
  DERIVED_BREAKPOINTS,
  LayoutError,
  SIZES,
  SIZE_IDS,
  addSection,
  clearOverrides,
  createBoard,
  move,
  moveSection,
  nudge,
  place,
  remove,
  removeSection,
  resize,
  setSectionSpan,
  smartInsert,
  type Board,
  type Breakpoint,
  type DerivedBreakpoint,
  type DerivedLayout,
  type Direction,
  type Item,
  type ItemInput,
  type SectionInput,
  type SectionOverride,
  type SizeId,
} from '../src/index.ts';

// ---------------------------------------------------------------------------
// Builders
// ---------------------------------------------------------------------------

export function item(id: string, size: SizeId, supportedSizes: readonly SizeId[] = [size], type = 'test.widget'): ItemInput {
  return { id, type, size, supportedSizes };
}

export function section(id: string, items: readonly ItemInput[], span = 12, extra: Partial<SectionInput> = {}): SectionInput {
  return { id, span, items, ...extra };
}

/** Positions of a section's items as "id@x,y" strings, in order. */
export function positions(board: Board, sectionId: string): string[] {
  const s = board.sections.find((sec) => sec.id === sectionId);
  if (s === undefined) throw new Error(`no section ${sectionId}`);
  return s.items.map((i) => `${i.id}@${i.x},${i.y}`);
}

export function allItems(board: Board): Item[] {
  return board.sections.flatMap((s) => [...s.items]);
}

// ---------------------------------------------------------------------------
// Independent oracles (deliberately not using the engine's own checks)
// ---------------------------------------------------------------------------

/** Paint every cell; report overlaps and out-of-bounds. */
export function geometryErrors(layout: DerivedLayout): string[] {
  const errors: string[] = [];
  for (const sec of layout.sections) {
    if (sec.column < 0 || sec.column + sec.span > layout.columns) errors.push(`${sec.id} overflows the board`);
    const painted = new Map<string, string>();
    for (const p of sec.items) {
      if (p.x < 0 || p.y < 0 || p.x + p.w > sec.span) errors.push(`${p.id} out of bounds in ${sec.id}`);
      if (p.w !== SIZES[p.size].w || p.h !== SIZES[p.size].h) errors.push(`${p.id} extent mismatch`);
      for (let dx = 0; dx < p.w; dx++) {
        for (let dy = 0; dy < p.h; dy++) {
          const key = `${p.x + dx},${p.y + dy}`;
          const other = painted.get(key);
          if (other !== undefined) errors.push(`${p.id} overlaps ${other} at ${key}`);
          painted.set(key, p.id);
        }
      }
    }
  }
  return errors;
}

/**
 * Reference implementation of CSS grid `row dense` auto-placement, written
 * the slow, obvious way (a set of painted cells, scan from the origin).
 */
export function cssDense(extents: readonly { w: number; h: number }[], columns: number): { x: number; y: number }[] {
  const taken = new Set<string>();
  return extents.map(({ w, h }) => {
    for (let y = 0; ; y++) {
      for (let x = 0; x + w <= columns; x++) {
        let free = true;
        for (let dx = 0; dx < w && free; dx++) {
          for (let dy = 0; dy < h && free; dy++) if (taken.has(`${x + dx},${y + dy}`)) free = false;
        }
        if (free) {
          for (let dx = 0; dx < w; dx++) for (let dy = 0; dy < h; dy++) taken.add(`${x + dx},${y + dy}`);
          return { x, y };
        }
      }
    }
  });
}

// ---------------------------------------------------------------------------
// Arbitraries
// ---------------------------------------------------------------------------

export const arbSize = fc.constantFrom(...SIZE_IDS);
export const arbBreakpoint = fc.constantFrom<Breakpoint>(...BREAKPOINTS);
export const arbDerivedBreakpoint = fc.constantFrom<DerivedBreakpoint>(...DERIVED_BREAKPOINTS);
export const arbSpan = fc.oneof(fc.constantFrom(4, 6, 8, 12), fc.integer({ min: 1, max: 12 }));

/** A widget's declared sizes plus a seed used to pick its current size. */
export interface ItemSpec {
  readonly supported: readonly SizeId[];
  readonly pick: number;
  readonly type: string;
}

export const arbItemSpec: fc.Arbitrary<ItemSpec> = fc.record({
  supported: fc.uniqueArray(arbSize, { minLength: 1, maxLength: 5 }),
  pick: fc.nat(),
  type: fc.constantFrom('proxmox.node', 'docker.container', 'clock', 'camera', 'sonarr'),
});

/** Turn a spec into a valid item for a section `span` wide. */
export function specToItem(id: string, spec: ItemSpec, span: number): ItemInput {
  let fitting = spec.supported.filter((s) => SIZES[s].w <= span);
  let supported = spec.supported;
  if (fitting.length === 0) {
    supported = [...supported, 'glyph'];
    fitting = ['glyph'];
  }
  return { id, type: spec.type, supportedSizes: supported, size: fitting[spec.pick % fitting.length] as SizeId };
}

interface RawOverride {
  readonly span: number | null;
  readonly order: readonly number[] | null;
  readonly sizes: readonly { readonly pick: number; readonly size: SizeId }[] | null;
}

const arbRawOverride: fc.Arbitrary<RawOverride | null> = fc.option(
  fc.record({
    span: fc.option(fc.integer({ min: 1, max: 8 })),
    order: fc.option(fc.array(fc.nat({ max: 20 }), { maxLength: 16 })),
    sizes: fc.option(fc.array(fc.record({ pick: fc.nat(), size: arbSize }), { maxLength: 4 })),
  }),
  { freq: 3 },
);

/** Random but valid board input, including raw (possibly stale) overrides. */
export const arbBoardInput = fc
  .array(
    fc.record({
      span: arbSpan,
      title: fc.option(fc.string({ maxLength: 12 })),
      items: fc.array(arbItemSpec, { maxLength: 14 }),
      tablet: arbRawOverride,
      phone: arbRawOverride,
    }),
    { maxLength: 4 },
  )
  .map((raw) =>
    raw.map((s, si): SectionInput => {
      const items = s.items.map((spec, ii) => specToItem(`s${si}i${ii}`, spec, s.span));
      const ids = items.map((i) => i.id);
      const overrides: { [K in DerivedBreakpoint]?: SectionOverride } = {};
      for (const bp of DERIVED_BREAKPOINTS) {
        const r = s[bp];
        if (r === null) continue;
        const o: { -readonly [K in keyof SectionOverride]: SectionOverride[K] } = {};
        if (r.span !== null) o.span = Math.min(r.span, BREAKPOINT_COLUMNS[bp]);
        // Indices past the item count become stale ids, which must be dropped.
        if (r.order !== null) o.order = r.order.map((n) => ids[n] ?? `ghost${n}`);
        if (r.sizes !== null && ids.length > 0) {
          o.sizes = Object.fromEntries(r.sizes.map(({ pick, size }) => [ids[pick % ids.length] as string, size]));
        }
        overrides[bp] = o;
      }
      return {
        id: `s${si}`,
        span: s.span,
        items,
        ...(s.title === null ? {} : { title: s.title }),
        ...(Object.keys(overrides).length > 0 ? { overrides } : {}),
      };
    }),
  );

export const arbBoard: fc.Arbitrary<Board> = arbBoardInput.map((sections) => createBoard({ sections }));

// ---------------------------------------------------------------------------
// Random operations
// ---------------------------------------------------------------------------

export type Op =
  | { readonly kind: 'place'; readonly sec: number; readonly x: number; readonly y: number; readonly spec: ItemSpec }
  | { readonly kind: 'smart'; readonly sec: number | null; readonly near: { sec: number; y: number } | null; readonly spec: ItemSpec }
  | { readonly kind: 'move'; readonly pick: number; readonly sec: number | null; readonly x: number; readonly y: number; readonly bp: Breakpoint }
  | { readonly kind: 'resize'; readonly pick: number; readonly size: SizeId; readonly own: number | null; readonly bp: Breakpoint }
  | { readonly kind: 'remove'; readonly pick: number }
  | { readonly kind: 'nudge'; readonly pick: number; readonly dir: Direction; readonly bp: Breakpoint }
  | { readonly kind: 'span'; readonly sec: number; readonly span: number; readonly bp: Breakpoint }
  | { readonly kind: 'addSection'; readonly span: number; readonly index: number }
  | { readonly kind: 'removeSection'; readonly sec: number }
  | { readonly kind: 'moveSection'; readonly sec: number; readonly to: number }
  | { readonly kind: 'clear'; readonly sec: number | null; readonly bp: DerivedBreakpoint | null };

const coord = fc.oneof(
  fc.integer({ min: -2, max: 14 }),
  fc.double({ min: -5, max: 40, noNaN: true }),
  fc.constantFrom(Infinity, -Infinity),
);

export const arbOp: fc.Arbitrary<Op> = fc.oneof(
  { arbitrary: fc.record({ kind: fc.constant('place' as const), sec: fc.nat(), x: coord, y: coord, spec: arbItemSpec }), weight: 3 },
  {
    arbitrary: fc.record({
      kind: fc.constant('smart' as const),
      sec: fc.option(fc.nat(), { freq: 3 }),
      near: fc.option(fc.record({ sec: fc.nat(), y: fc.integer({ min: -1, max: 30 }) })),
      spec: arbItemSpec,
    }),
    weight: 3,
  },
  {
    arbitrary: fc.record({
      kind: fc.constant('move' as const),
      pick: fc.nat(),
      sec: fc.option(fc.nat(), { freq: 2 }),
      x: coord,
      y: coord,
      bp: arbBreakpoint,
    }),
    weight: 5,
  },
  {
    // `own` (when set) picks from the item's declared sizes; otherwise any size, often unsupported.
    arbitrary: fc.record({ kind: fc.constant('resize' as const), pick: fc.nat(), size: arbSize, own: fc.option(fc.nat(), { freq: 3 }), bp: arbBreakpoint }),
    weight: 4,
  },
  { arbitrary: fc.record({ kind: fc.constant('remove' as const), pick: fc.nat() }), weight: 2 },
  {
    arbitrary: fc.record({
      kind: fc.constant('nudge' as const),
      pick: fc.nat(),
      dir: fc.constantFrom<Direction>('left', 'right', 'up', 'down'),
      bp: arbBreakpoint,
    }),
    weight: 3,
  },
  {
    arbitrary: fc.record({ kind: fc.constant('span' as const), sec: fc.nat(), span: fc.integer({ min: 0, max: 13 }), bp: arbBreakpoint }),
    weight: 2,
  },
  { arbitrary: fc.record({ kind: fc.constant('addSection' as const), span: arbSpan, index: fc.integer({ min: -1, max: 5 }) }), weight: 1 },
  { arbitrary: fc.record({ kind: fc.constant('removeSection' as const), sec: fc.nat() }), weight: 1 },
  { arbitrary: fc.record({ kind: fc.constant('moveSection' as const), sec: fc.nat(), to: fc.integer({ min: -1, max: 5 }) }), weight: 1 },
  {
    arbitrary: fc.record({ kind: fc.constant('clear' as const), sec: fc.option(fc.nat()), bp: fc.option(arbDerivedBreakpoint) }),
    weight: 1,
  },
);

/** Errors an operation may legitimately refuse with. Anything else is a bug. */
const EXPECTED_REFUSALS = new Set(['UNSUPPORTED_SIZE', 'NO_FIT', 'CROSS_SECTION_AT_BREAKPOINT', 'INVALID_ARGUMENT', 'NO_SECTION']);

let fresh = 0;

/**
 * Apply one random operation. Expected refusals return the input board;
 * any other exception propagates and fails the property.
 */
export function applyOp(board: Board, op: Op): Board {
  const items = allItems(board);
  const sections = board.sections;
  const pickItem = (n: number): Item | undefined => items[n % Math.max(1, items.length)];
  const pickSection = (n: number): string | undefined => sections[n % Math.max(1, sections.length)]?.id;
  try {
    switch (op.kind) {
      case 'place': {
        const sectionId = pickSection(op.sec);
        if (sectionId === undefined) return board;
        const span = sections.find((s) => s.id === sectionId)?.span ?? 12;
        return place(board, specToItem(`new${fresh++}`, op.spec, span), { sectionId, x: op.x, y: op.y });
      }
      case 'smart': {
        const sectionId = op.sec === null ? undefined : pickSection(op.sec);
        const nearId = op.near === null ? undefined : pickSection(op.near.sec);
        const span = sections.find((s) => s.id === (sectionId ?? nearId))?.span ?? 12;
        const input = specToItem(`new${fresh++}`, op.spec, span);
        return smartInsert(board, input, {
          ...(sectionId === undefined ? {} : { sectionId }),
          ...(nearId === undefined || op.near === null ? {} : { near: { sectionId: nearId, y: op.near.y } }),
        });
      }
      case 'move': {
        const target = pickItem(op.pick);
        if (target === undefined) return board;
        const sectionId = op.sec === null ? undefined : pickSection(op.sec);
        return move(board, target.id, { ...(sectionId === undefined ? {} : { sectionId }), x: op.x, y: op.y }, { breakpoint: op.bp });
      }
      case 'resize': {
        const target = pickItem(op.pick);
        if (target === undefined) return board;
        const others = target.supportedSizes.filter((s) => s !== target.size);
        const pool = others.length > 0 ? others : target.supportedSizes;
        const size = op.own === null ? op.size : (pool[op.own % pool.length] as SizeId);
        return resize(board, target.id, size, { breakpoint: op.bp });
      }
      case 'remove': {
        const target = pickItem(op.pick);
        return target === undefined ? board : remove(board, target.id);
      }
      case 'nudge': {
        const target = pickItem(op.pick);
        return target === undefined ? board : nudge(board, target.id, op.dir, { breakpoint: op.bp });
      }
      case 'span': {
        const sectionId = pickSection(op.sec);
        return sectionId === undefined ? board : setSectionSpan(board, sectionId, op.span, { breakpoint: op.bp });
      }
      case 'addSection':
        return addSection(board, { id: `sec${fresh++}`, span: op.span }, op.index);
      case 'removeSection': {
        const sectionId = pickSection(op.sec);
        return sectionId === undefined ? board : removeSection(board, sectionId);
      }
      case 'moveSection': {
        const sectionId = pickSection(op.sec);
        return sectionId === undefined ? board : moveSection(board, sectionId, op.to);
      }
      case 'clear': {
        const sectionId = op.sec === null ? undefined : pickSection(op.sec);
        return clearOverrides(board, {
          ...(sectionId === undefined ? {} : { sectionId }),
          ...(op.bp === null ? {} : { breakpoint: op.bp }),
        });
      }
    }
  } catch (error) {
    if (error instanceof LayoutError && EXPECTED_REFUSALS.has(error.code)) return board;
    throw error;
  }
}

/**
 * fast-check run counts, kept in one place so they're easy to quote.
 * `LAYOUT_RUNS_SCALE=10 bun test` runs a deeper soak.
 */
const scale = Math.max(1, Number(process.env['LAYOUT_RUNS_SCALE'] ?? 1) || 1);
export const RUNS = {
  /** Scenario properties: a random board plus up to 30 random operations. */
  heavy: 400 * scale,
  /** Cheaper single-step properties. */
  standard: 1000 * scale,
} as const;
