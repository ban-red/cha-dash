import type { Breakpoint } from '@chadash/layout';
import { onScopeDispose, readonly, shallowRef, toValue, watch, type MaybeRefOrGetter, type Ref } from 'vue';

/**
 * Minimum container width (px) for each breakpoint. These match the
 * prototype's container queries (`max-width: 640px` is the 4-column phone
 * layout, `max-width: 1040px` the 8-column tablet layout). 1040 px is about
 * where 12 columns reach the minimum unit size (12 × 76 px plus 11 gutters).
 */
export const BREAKPOINT_MIN_WIDTHS: { readonly tablet: number; readonly desktop: number } = Object.freeze({
  tablet: 641,
  desktop: 1041,
});

export type BreakpointMinWidths = { readonly tablet: number; readonly desktop: number };

/** The breakpoint for a container width: 12, 8 or 4 columns. */
export function breakpointForWidth(width: number, minWidths: BreakpointMinWidths = BREAKPOINT_MIN_WIDTHS): Breakpoint {
  if (width >= minWidths.desktop) return 'desktop';
  if (width >= minWidths.tablet) return 'tablet';
  return 'phone';
}

export interface UseBreakpointOptions {
  /** Override the thresholds. */
  readonly minWidths?: BreakpointMinWidths;
  /** The breakpoint before the element is measured (and during SSR). Defaults to `desktop`. */
  readonly initial?: Breakpoint;
}

export interface UseBreakpoint {
  readonly breakpoint: Readonly<Ref<Breakpoint>>;
  /** The observed inline size in px (0 until measured). */
  readonly width: Readonly<Ref<number>>;
}

/**
 * Track the layout breakpoint of a container from its width, with a
 * ResizeObserver. Pass a template ref (or getter) for the element; the
 * observer follows it when it changes and stops with the calling scope.
 */
export function useBreakpoint(
  target: MaybeRefOrGetter<Element | null | undefined>,
  options: UseBreakpointOptions = {},
): UseBreakpoint {
  const minWidths = options.minWidths ?? BREAKPOINT_MIN_WIDTHS;
  const width = shallowRef(0);
  const breakpoint = shallowRef<Breakpoint>(options.initial ?? 'desktop');

  const update = (w: number): void => {
    width.value = w;
    breakpoint.value = breakpointForWidth(w, minWidths);
  };

  let observer: ResizeObserver | undefined;
  let stopWindow: (() => void) | undefined;

  const stop = (): void => {
    observer?.disconnect();
    observer = undefined;
    stopWindow?.();
    stopWindow = undefined;
  };

  watch(
    () => toValue(target),
    (el) => {
      stop();
      if (el === null || el === undefined) return;
      update(el.getBoundingClientRect().width);
      if (typeof ResizeObserver === 'function') {
        observer = new ResizeObserver((entries) => {
          const entry = entries[entries.length - 1];
          if (entry === undefined) return;
          const box = entry.contentBoxSize?.[0];
          update(box === undefined ? entry.contentRect.width : box.inlineSize);
        });
        observer.observe(el);
      } else if (typeof window !== 'undefined') {
        const onResize = (): void => update(el.getBoundingClientRect().width);
        window.addEventListener('resize', onResize);
        stopWindow = () => window.removeEventListener('resize', onResize);
      }
    },
    { immediate: true, flush: 'post' },
  );

  onScopeDispose(stop);

  return { breakpoint: readonly(breakpoint), width: readonly(width) };
}
