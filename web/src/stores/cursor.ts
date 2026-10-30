/**
 * The page's time cursor (01 · Vision → the Pulse strip, linked scrubbing):
 * a moment in the last day that the board shows while someone scrubs.
 * Every board sparkline marks its value then; scrubbing the Pulse strip
 * also shows each tile's health then. undefined is now.
 *
 * Times are Unix ms on this browser's clock, as the sparklines' "6 h ago" is.
 */
import { shallowRef } from 'vue';
import type { PulseReply } from '../model/history';

export interface Cursor {
  at: number;
  /** Who set it, so the one scrubbing doesn't follow itself: 'pulse' for the Pulse strip, a sparkline's own token. */
  source: string | object;
}

/** Shallow, so a source token stays itself rather than a reactive copy. */
export const cursor = shallowRef<Cursor | undefined>();

export function setCursor(at: number, source: string | object): void {
  if (cursor.value?.at !== at || cursor.value.source !== source) cursor.value = { at, source };
}

/** Clears the cursor, if source still holds it. */
export function clearCursor(source: string | object): void {
  if (cursor.value?.source === source) cursor.value = undefined;
}

/** The Pulse strip's latest reply, which tiles read their past health from. */
export const pulse = shallowRef<PulseReply | undefined>();
