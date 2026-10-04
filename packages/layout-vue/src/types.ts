import type { Board, Breakpoint, Item, PlacedItem, PlacedSection, SizeId } from '@chadash/layout';
import type { LayoutMessages } from './messages.ts';
import type { MotionPreference } from './springs.ts';

export interface LayoutBoardProps {
  /** The board to render. Treated as immutable; the component never mutates it. */
  board: Board;
  /** Edit mode: items lift, the unit grid shows, and drag, resize and keyboard editing are on. */
  editing?: boolean;
  /**
   * Which layout to render and edit. Omit it to follow the container width
   * (12, 8 or 4 columns; see `BREAKPOINT_MIN_WIDTHS`). Edits at `tablet` and
   * `phone` are stored as overrides by the engine.
   */
  breakpoint?: Breakpoint;
  /** The name announced for an item ("Plex"). Defaults to the item id. */
  itemLabel?: (item: Item) => string;
  /** The name shown and announced for a section. Defaults to its title, then its id. */
  sectionLabel?: (section: PlacedSection) => string;
  /** Override any announcement or label (see `LayoutMessages`). */
  messages?: Partial<LayoutMessages>;
  /** Force a motion preference. `auto` (the default) follows `data-motion` on `<html>`, then the OS. */
  motion?: 'auto' | MotionPreference;
}

/** Events. Every board is a new immutable `Board` made by `@chadash/layout` operations. */
export type LayoutBoardEmits = {
  /** After each preview step or operation (drag preview, nudge, resize, cancel). Supports `v-model:board`. */
  'update:board': [board: Board];
  /** Once when a gesture ends with a changed board, or after a one-shot keyboard resize. Record it in history. */
  commit: [board: Board];
  /** An item received focus. */
  select: [id: string];
};

/** Props of the `#item` slot. */
export interface ItemSlotProps {
  /** The canonical item (id, type, canonical size, supported sizes). */
  item: Item;
  /** The size the item shows at the current breakpoint. Render the layout for this size. */
  size: SizeId;
  /** True in edit mode. */
  editing: boolean;
  /** True while this item is picked up, dragged or resized. */
  dragging: boolean;
  /** The placed item at this breakpoint (x, y, w, h and `forced`). */
  placed: PlacedItem;
}

/** Props of the `#section` slot (a section's header). */
export interface SectionSlotProps {
  section: PlacedSection;
  editing: boolean;
}
