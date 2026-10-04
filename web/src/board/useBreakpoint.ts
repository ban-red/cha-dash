/**
 * The board's breakpoint from its own width (container, not viewport): 12
 * columns from 1040 px, 8 from 640 px, else 4. Matches the prototype's
 * container queries.
 */
import { onBeforeUnmount, onMounted, ref, type Ref } from 'vue';
import type { Breakpoint } from '@chadash/layout';

export const TABLET_MIN = 640;
export const DESKTOP_MIN = 1040;

export function breakpointFor(width: number): Breakpoint {
  if (width >= DESKTOP_MIN) return 'desktop';
  if (width >= TABLET_MIN) return 'tablet';
  return 'phone';
}

export function useBreakpoint(el: Ref<HTMLElement | null>, initial: Breakpoint = 'desktop') {
  const breakpoint = ref<Breakpoint>(initial);
  const width = ref(0);
  let ro: ResizeObserver | undefined;

  const measure = (w: number) => {
    if (!w) return;
    width.value = w;
    breakpoint.value = breakpointFor(w);
  };

  onMounted(() => {
    const node = el.value;
    if (!node) return;
    measure(node.getBoundingClientRect().width);
    if (typeof ResizeObserver === 'function') {
      ro = new ResizeObserver((entries) => {
        for (const e of entries) measure(e.contentRect.width);
      });
      ro.observe(node);
    }
  });
  onBeforeUnmount(() => ro?.disconnect());

  return { breakpoint, width };
}
