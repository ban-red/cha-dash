/**
 * Motion tokens for script-driven animation (motion-v). Springs are duration +
 * bounce (WWDC23), the same model the CSS tokens sample into `linear()`:
 *
 *   press 0.2 s · 0   move 0.35 s · 0   lift 0.3 s · 0.15   sheet 0.45 s · 0.08
 *
 * Under reduced motion everything becomes the 0.15 s fade.
 */
import { onScopeDispose, ref, type Ref } from 'vue';

export const SPRING = {
  press: { type: 'spring', visualDuration: 0.2, bounce: 0 },
  move: { type: 'spring', visualDuration: 0.35, bounce: 0 },
  lift: { type: 'spring', visualDuration: 0.3, bounce: 0.15 },
  sheet: { type: 'spring', visualDuration: 0.45, bounce: 0.08 },
} as const;

export const FADE = { duration: 0.15, ease: 'easeOut' } as const;

export type SpringName = keyof typeof SPRING;

function query(): MediaQueryList | undefined {
  return typeof matchMedia === 'function' ? matchMedia('(prefers-reduced-motion: reduce)') : undefined;
}

/** True when the OS asks for less motion, unless the page forces full motion. */
export function prefersReducedMotion(): boolean {
  const forced = typeof document !== 'undefined' ? document.documentElement.dataset['motion'] : undefined;
  if (forced === 'full') return false;
  if (forced === 'reduced' || forced === 'none') return true;
  return Boolean(query()?.matches);
}

/** Reactive reduced-motion flag for components. */
export function useReducedMotion(): Ref<boolean> {
  const r = ref(prefersReducedMotion());
  const mq = query();
  const on = () => (r.value = prefersReducedMotion());
  mq?.addEventListener?.('change', on);
  onScopeDispose(() => mq?.removeEventListener?.('change', on));
  return r;
}

/** The transition for a spring token, or the fade when motion is reduced. */
export function transition(name: SpringName, reduced = prefersReducedMotion()) {
  return reduced ? FADE : SPRING[name];
}
