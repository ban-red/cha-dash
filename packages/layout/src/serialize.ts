import { buildSection, structureProblems } from './board.ts';
import { LayoutError } from './errors.ts';
import type { SizeId } from './sizes.ts';
import {
  CANONICAL_COLUMNS,
  DERIVED_BREAKPOINTS,
  FLOWS,
  type Board,
  type DerivedBreakpoint,
  type Flow,
  type Section,
  type SectionInput,
} from './types.ts';

/** Version written by `toDocument`/`serialize`. */
export const LAYOUT_VERSION = 1;

export interface ItemDocument {
  readonly id: string;
  readonly type: string;
  readonly size: SizeId;
  readonly supportedSizes: readonly SizeId[];
  /** Informational: recomputed from item order on load. */
  readonly x: number;
  /** Informational: recomputed from item order on load. */
  readonly y: number;
}

export interface SectionOverrideDocument {
  readonly span?: number;
  readonly order?: readonly string[];
  readonly sizes?: { readonly [itemId: string]: SizeId };
}

export interface SectionDocument {
  readonly id: string;
  readonly title?: string;
  readonly span: number;
  /** In Z order. The order is authoritative; x/y are derived from it. */
  readonly items: readonly ItemDocument[];
  readonly overrides?: { readonly [K in DerivedBreakpoint]?: SectionOverrideDocument };
}

/** Version 1 of the stored layout format (what the hub keeps as JSON). */
export interface LayoutDocument {
  readonly version: typeof LAYOUT_VERSION;
  /** Canonical board columns. Always 12 in version 1. */
  readonly columns: typeof CANONICAL_COLUMNS;
  /** How sections share the board. Written only when it isn't `sections`. */
  readonly flow?: Exclude<Flow, 'sections'>;
  readonly sections: readonly SectionDocument[];
}

/**
 * Upgrades a document from version N to N + 1. It receives a plain object
 * with `version: N` and must return one with `version: N + 1`.
 */
export type Migration = (document: Record<string, unknown>) => unknown;

/** Migrations keyed by the version they upgrade from. */
export interface Migrations {
  readonly [fromVersion: number]: Migration;
}

/** Built-in migrations. Empty while version 1 is the only format. */
export const MIGRATIONS: Migrations = Object.freeze({});

export interface ReadOptions {
  /** Extra or replacement migrations, consulted before the built-in ones. */
  readonly migrations?: Migrations;
}

function sectionDocument(section: Section): SectionDocument {
  const items = section.items.map(
    (item): ItemDocument => ({
      id: item.id,
      type: item.type,
      size: item.size,
      supportedSizes: [...item.supportedSizes],
      x: item.x,
      y: item.y,
    }),
  );
  let overrides: { [K in DerivedBreakpoint]?: SectionOverrideDocument } | undefined;
  for (const bp of DERIVED_BREAKPOINTS) {
    const o = section.overrides?.[bp];
    if (o === undefined) continue;
    const out: { -readonly [K in keyof SectionOverrideDocument]: SectionOverrideDocument[K] } = {};
    if (o.span !== undefined) out.span = o.span;
    if (o.order !== undefined) out.order = [...o.order];
    if (o.sizes !== undefined) {
      // Stable key order (canonical item order) so equal boards serialize identically.
      const sizes: { [id: string]: SizeId } = {};
      for (const item of section.items) {
        const size = o.sizes[item.id];
        if (size !== undefined) sizes[item.id] = size;
      }
      out.sizes = sizes;
    }
    (overrides ??= {})[bp] = out;
  }
  const base = section.title === undefined
    ? { id: section.id, span: section.span, items }
    : { id: section.id, title: section.title, span: section.span, items };
  return overrides === undefined ? base : { ...base, overrides };
}

/** Convert a board to its plain, versioned document form. */
export function toDocument(board: Board): LayoutDocument {
  const sections = board.sections.map(sectionDocument);
  return board.flow === undefined || board.flow === 'sections'
    ? { version: LAYOUT_VERSION, columns: CANONICAL_COLUMNS, sections }
    : { version: LAYOUT_VERSION, columns: CANONICAL_COLUMNS, flow: board.flow, sections };
}

/** Serialize a board to JSON. Equal boards produce identical strings. */
export function serialize(board: Board, options: { readonly space?: number } = {}): string {
  return JSON.stringify(toDocument(board), null, options.space);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function invalid(message: string): LayoutError {
  return new LayoutError('INVALID_DOCUMENT', message);
}

function migrate(input: Record<string, unknown>, options: ReadOptions): Record<string, unknown> {
  let document = input;
  const declared = document['version'];
  if (typeof declared !== 'number' || !Number.isInteger(declared) || declared < 0) {
    throw invalid('layout document has no valid "version"');
  }
  let version: number = declared;
  if (version > LAYOUT_VERSION) {
    throw new LayoutError(
      'UNSUPPORTED_VERSION',
      `layout document version ${version} is newer than this engine supports (${LAYOUT_VERSION})`,
    );
  }
  while (version < LAYOUT_VERSION) {
    const step: Migration | undefined = options.migrations?.[version] ?? MIGRATIONS[version];
    if (step === undefined) {
      throw new LayoutError('UNSUPPORTED_VERSION', `no migration from layout document version ${version}`);
    }
    const next = step(document);
    if (!isRecord(next) || next['version'] !== version + 1) {
      throw invalid(`migration from version ${version} must return a document with version ${version + 1}`);
    }
    document = next;
    version = version + 1;
  }
  return document;
}

/**
 * Read a document (any supported version) into a board. Older versions are
 * migrated first. Item positions are recomputed from item order; stale
 * override references are dropped. Throws `INVALID_DOCUMENT` or
 * `UNSUPPORTED_VERSION`.
 */
export function fromDocument(input: unknown, options: ReadOptions = {}): Board {
  if (!isRecord(input)) throw invalid('layout document must be an object');
  const document = migrate(input, options);
  if (document['columns'] !== CANONICAL_COLUMNS) {
    throw invalid(`layout document "columns" must be ${CANONICAL_COLUMNS}`);
  }
  const sections = document['sections'];
  if (!Array.isArray(sections)) throw invalid('layout document "sections" must be an array');
  sections.forEach((section: unknown, s) => {
    if (!isRecord(section)) throw invalid(`sections[${s}] must be an object`);
    const items = section['items'];
    if (!Array.isArray(items)) throw invalid(`sections[${s}].items must be an array`);
    items.forEach((item: unknown, i) => {
      if (!isRecord(item)) throw invalid(`sections[${s}].items[${i}] must be an object`);
    });
    if (section['span'] === undefined) throw invalid(`sections[${s}].span is required`);
  });
  const inputs = sections as SectionInput[];
  const problems = structureProblems(inputs);
  if (problems.length > 0) throw invalid(`invalid layout document:\n- ${problems.join('\n- ')}`);
  // A flow this engine doesn't know (a newer app's) reads as the default rather than failing the board.
  const flow = document['flow'];
  const known = typeof flow === 'string' && (FLOWS as readonly string[]).includes(flow) && flow !== 'sections';
  return known ? { sections: inputs.map(buildSection), flow: flow as Flow } : { sections: inputs.map(buildSection) };
}

/** Parse JSON produced by `serialize` (or an older version of it). */
export function deserialize(json: string, options: ReadOptions = {}): Board {
  let parsed: unknown;
  try {
    parsed = JSON.parse(json);
  } catch (error) {
    throw invalid(`layout document is not valid JSON: ${error instanceof Error ? error.message : String(error)}`);
  }
  return fromDocument(parsed, options);
}
