import {
  buildSection,
  findItem,
  isValidSpan,
  locateSection,
  packItems,
  replaceSection,
  structureProblems,
  withOverrides,
} from './board.ts';
import { LayoutError } from './errors.ts';
import { SIZES, sizeFits } from './sizes.ts';
import {
  BREAKPOINT_COLUMNS,
  DERIVED_BREAKPOINTS,
  type Board,
  type DerivedBreakpoint,
  type Section,
  type SectionInput,
  type SectionOverride,
} from './types.ts';
import type { OpOptions } from './operations.ts';

/** Insert a new section (at the end by default). */
export function addSection(board: Board, input: SectionInput, index: number = board.sections.length): Board {
  if (board.sections.some((section) => section.id === input.id)) {
    throw new LayoutError('DUPLICATE_ID', `a section with id "${input.id}" already exists`);
  }
  for (const item of input.items ?? []) {
    if (findItem(board, item.id) !== undefined) {
      throw new LayoutError('DUPLICATE_ID', `an item with id "${item.id}" already exists`);
    }
  }
  const problems = structureProblems([input]);
  if (problems.length > 0) throw new LayoutError('INVALID_ARGUMENT', problems.join('; '));
  const at = Math.min(Math.max(0, Math.trunc(index)), board.sections.length);
  const sections = board.sections.slice();
  sections.splice(at, 0, buildSection(input));
  return { ...board, sections };
}

/** Remove a section and every item in it. */
export function removeSection(board: Board, sectionId: string): Board {
  const index = locateSection(board, sectionId);
  return { ...board, sections: board.sections.filter((_, i) => i !== index) };
}

/** Move a section so it ends up at `toIndex` (clamped). */
export function moveSection(board: Board, sectionId: string, toIndex: number): Board {
  const from = locateSection(board, sectionId);
  const to = Math.min(Math.max(0, Math.trunc(toIndex)), board.sections.length - 1);
  if (from === to) return board;
  const sections = board.sections.slice();
  const [section] = sections.splice(from, 1);
  sections.splice(to, 0, section as Section);
  return { ...board, sections };
}

/** Set or clear (with undefined or an empty string) a section's title. */
export function setSectionTitle(board: Board, sectionId: string, title: string | undefined): Board {
  const index = locateSection(board, sectionId);
  const section = board.sections[index] as Section;
  const next = title === undefined || title === '' ? undefined : title;
  if (section.title === next) return board;
  const { title: _old, ...rest } = section;
  return replaceSection(board, index, next === undefined ? rest : { ...rest, title: next });
}

/**
 * Change a section's width.
 *
 * At `desktop` this changes the canonical span (and internal columns); every
 * item must already fit, otherwise `NO_FIT` (resize them first). At
 * `tablet`/`phone` it stores a span override, or clears it when the span
 * equals the derived default.
 */
export function setSectionSpan(board: Board, sectionId: string, span: number, options: OpOptions = {}): Board {
  const index = locateSection(board, sectionId);
  const section = board.sections[index] as Section;
  const breakpoint = options.breakpoint ?? 'desktop';
  const max = BREAKPOINT_COLUMNS[breakpoint];
  if (!isValidSpan(span, max)) {
    throw new LayoutError('INVALID_ARGUMENT', `span must be an integer in 1..${max}`);
  }
  if (breakpoint === 'desktop') {
    if (span === section.span) return board;
    const tooWide = section.items.find((item) => !sizeFits(item.size, span));
    if (tooWide !== undefined) {
      throw new LayoutError(
        'NO_FIT',
        `item "${tooWide.id}" is ${SIZES[tooWide.size].w} columns wide and doesn't fit a span of ${span}`,
      );
    }
    const resized: Section = { ...section, span };
    return replaceSection(board, index, { ...resized, items: packItems(section.items, span) });
  }
  const derived = Math.min(section.span, max);
  const desired = span === derived ? undefined : span;
  const current = section.overrides?.[breakpoint];
  if (current?.span === desired) return board;
  const next: { -readonly [K in keyof SectionOverride]: SectionOverride[K] } = { ...current };
  if (desired === undefined) delete next.span;
  else next.span = desired;
  const overrides: { -readonly [K in DerivedBreakpoint]?: SectionOverride } = { ...section.overrides };
  if (Object.keys(next).length === 0) delete overrides[breakpoint];
  else overrides[breakpoint] = next;
  return replaceSection(
    board,
    index,
    withOverrides(section, Object.keys(overrides).length === 0 ? undefined : overrides),
  );
}

export interface ClearOverridesOptions {
  /** Only this section (default: all). */
  readonly sectionId?: string;
  /** Only this breakpoint (default: tablet and phone). */
  readonly breakpoint?: DerivedBreakpoint;
}

/** Drop per-breakpoint overrides so those layouts are fully derived again. */
export function clearOverrides(board: Board, options: ClearOverridesOptions = {}): Board {
  const only = options.sectionId === undefined ? undefined : locateSection(board, options.sectionId);
  const breakpoints = options.breakpoint === undefined ? DERIVED_BREAKPOINTS : [options.breakpoint];
  let changed = false;
  const sections = board.sections.map((section, i) => {
    if ((only !== undefined && i !== only) || section.overrides === undefined) return section;
    const overrides: { -readonly [K in DerivedBreakpoint]?: SectionOverride } = { ...section.overrides };
    for (const bp of breakpoints) delete overrides[bp];
    const next = withOverrides(section, Object.keys(overrides).length === 0 ? undefined : overrides);
    if (Object.keys(overrides).length === Object.keys(section.overrides).length) return section;
    changed = true;
    return next;
  });
  return changed ? { ...board, sections } : board;
}

