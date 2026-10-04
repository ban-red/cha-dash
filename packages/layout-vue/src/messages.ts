import type { Direction, SizeStep } from '@chadash/layout';

/** Where an item sits, for announcements. Row and column are 1-based, inside the section. */
export interface ItemPosition {
  readonly row: number;
  readonly column: number;
  /** The section's label, when the item changed section. */
  readonly section?: string;
}

/**
 * Every string the board speaks or labels. Override any of them through the
 * `messages` prop (for localisation, or the web app's own voice).
 */
export interface LayoutMessages {
  /** Accessible name of the board while editing. */
  readonly board: string;
  /** How screen readers describe an item while editing ("movable widget"). */
  readonly roleDescription: string;
  /** Instructions linked to every item while editing. */
  readonly instructions: string;
  /** Accessible name of an item while editing: "Plex, Wide". */
  readonly itemName: (label: string, sizeName: string) => string;
  readonly position: (position: ItemPosition) => string;
  readonly pickedUp: (label: string, position: string) => string;
  readonly moved: (label: string, position: string) => string;
  readonly blocked: (label: string, direction: Direction) => string;
  readonly resized: (label: string, sizeName: string) => string;
  readonly cannotResize: (label: string, step: SizeStep) => string;
  readonly dropped: (label: string, position: string) => string;
  readonly cancelled: (label: string, position: string) => string;
  /** A gesture was abandoned because the board changed underneath it. */
  readonly interrupted: (label: string) => string;
}

export const defaultMessages: LayoutMessages = Object.freeze<LayoutMessages>({
  board: 'Board layout',
  roleDescription: 'movable widget',
  instructions:
    'Arrow keys move between widgets. Press Enter or Space to pick a widget up, then use the arrow keys to move it ' +
    'and Shift with the arrow keys to resize it. Press Enter or Space again to drop it, or Escape to cancel.',
  itemName: (label, sizeName) => `${label}, ${sizeName}`,
  position: ({ row, column, section }) =>
    section === undefined ? `row ${row}, column ${column}` : `${section}, row ${row}, column ${column}`,
  pickedUp: (label, position) =>
    `Picked up ${label} at ${position}. Arrow keys move it, Shift and arrow keys resize it, Enter drops it, Escape cancels.`,
  moved: (label, position) => `Moved ${label} to ${position}`,
  blocked: (label, direction) => `${label} can't move further ${direction}`,
  resized: (_label, sizeName) => `Resized to ${sizeName}`,
  cannotResize: (label, step) => `${label} can't get any ${step}`,
  dropped: (label, position) => `Dropped ${label} at ${position}`,
  cancelled: (label, position) => `Cancelled. ${label} is back at ${position}`,
  interrupted: (label) => `The layout changed, so moving ${label} stopped`,
});
