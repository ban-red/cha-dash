/**
 * @chadash/layout-vue: the Vue 3.6 renderer for `@chadash/layout`.
 *
 * `<LayoutBoard>` renders a board and edits it (pointer drag with momentum,
 * spring reflow, the corner resize handle, keyboard editing, live
 * announcements). It renders no widget content: that's the `#item` slot.
 */

export { default as LayoutBoard } from './LayoutBoard.vue';

export type { ItemSlotProps, LayoutBoardEmits, LayoutBoardProps, SectionSlotProps } from './types.ts';

export { useLayoutHistory, type LayoutHistory, type UseLayoutHistoryOptions } from './useLayoutHistory.ts';

export {
  BREAKPOINT_MIN_WIDTHS,
  breakpointForWidth,
  useBreakpoint,
  type BreakpointMinWidths,
  type UseBreakpoint,
  type UseBreakpointOptions,
} from './useBreakpoint.ts';

export { defaultMessages, type ItemPosition, type LayoutMessages } from './messages.ts';

export { MOMENTUM, VelocityTracker, projectMomentum, projectionTime, type MomentumOptions, type Point } from './momentum.ts';

export {
  FADE_DURATION,
  SPRINGS,
  motionPreference,
  springTransition,
  type MotionPreference,
  type SpringName,
  type SpringSpec,
  type SpringTransition,
} from './springs.ts';

export { EDIT_SCALE, LIFT_SCALE } from './flip.ts';
