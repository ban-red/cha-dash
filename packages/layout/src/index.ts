/**
 * @chadash/layout: the Cha Dash layout engine.
 *
 * Pure, immutable, framework-agnostic. Every operation takes a board and
 * returns a new one (or the same object when nothing changed).
 */

export { LayoutError, type LayoutErrorCode } from './errors.ts';

export {
  SIZES,
  SIZE_IDS,
  autoSize,
  closestFit,
  isSizeId,
  sizeFits,
  sizeLabel,
  snapSize,
  stepSize,
  type SizeId,
  type SizeSpec,
  type SizeStep,
} from './sizes.ts';

export {
  BREAKPOINTS,
  BREAKPOINT_COLUMNS,
  CANONICAL_COLUMNS,
  DERIVED_BREAKPOINTS,
  FLOWS,
  TRACKS_PER_ROW,
  type Board,
  type BoardInput,
  type Breakpoint,
  type DerivedBreakpoint,
  type DerivedLayout,
  type Flow,
  type Item,
  type ItemInput,
  type PlacedItem,
  type PlacedSection,
  type Section,
  type SectionInput,
  type SectionOverride,
  type SectionOverrides,
  type TrackArea,
} from './types.ts';

export {
  createBoard,
  findItem,
  getItem,
  getSection,
  reflow,
  type ItemLocation,
} from './board.ts';

export {
  move,
  nudge,
  place,
  remove,
  resize,
  smartInsert,
  type Direction,
  type MoveTarget,
  type OpOptions,
  type SmartInsertOptions,
  type Target,
} from './operations.ts';

export {
  addSection,
  clearOverrides,
  moveSection,
  removeSection,
  setFlow,
  setSectionSpan,
  setSectionTitle,
  type ClearOverridesOptions,
} from './sections.ts';

export {
  deriveAll,
  deriveLayout,
  itemSize,
  MOSAIC_LABEL_COLUMNS,
  sectionColumns,
  sectionOrder,
  type DeriveOptions,
  type EffectiveSize,
} from './derive.ts';

export {
  canRedo,
  canUndo,
  commit,
  createHistory,
  redo,
  undo,
  DEFAULT_HISTORY_LIMIT,
  type History,
} from './history.ts';

export {
  LAYOUT_VERSION,
  MIGRATIONS,
  deserialize,
  fromDocument,
  serialize,
  toDocument,
  type ItemDocument,
  type LayoutDocument,
  type Migration,
  type Migrations,
  type ReadOptions,
  type SectionDocument,
  type SectionOverrideDocument,
} from './serialize.ts';

export { checkInvariants, checkLayout } from './invariants.ts';
