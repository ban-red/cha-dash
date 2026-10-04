/**
 * Motion tokens for script-driven springs.
 *
 * These mirror `MOTION` in `@chadash/tokens` (design/tokens/src/core.js): a
 * spring is a perceptual duration plus a bounce, the SwiftUI model from
 * WWDC23. A test keeps the two in sync. CSS transitions use the generated
 * `--motion-*` custom properties instead.
 */

export type SpringName = 'press' | 'move' | 'lift' | 'sheet';

export interface SpringSpec {
  /** Perceptual duration in seconds (sets the stiffness). */
  readonly duration: number;
  /** 0 = critically damped. Damping ratio = 1 - bounce. */
  readonly bounce: number;
}

export const SPRINGS: { readonly [K in SpringName]: SpringSpec } = Object.freeze({
  press: Object.freeze({ duration: 0.2, bounce: 0 }),
  move: Object.freeze({ duration: 0.35, bounce: 0 }),
  lift: Object.freeze({ duration: 0.3, bounce: 0.15 }),
  sheet: Object.freeze({ duration: 0.45, bounce: 0.08 }),
});

/** The reduced-motion crossfade (`--motion-fade-duration`), in seconds. */
export const FADE_DURATION = 0.15;

export interface SpringTransition {
  readonly type: 'spring';
  readonly stiffness: number;
  readonly damping: number;
  readonly mass: number;
  readonly velocity?: number;
  readonly restDelta: number;
  readonly restSpeed: number;
}

/**
 * A Motion spring transition with exactly the token physics:
 * ω = 2π / duration, ζ = 1 − bounce, so stiffness = ω² and damping = 2ζω
 * at mass 1. (Motion's own `visualDuration` scales the duration by 1.2, so
 * the physics are passed directly instead.)
 */
export function springTransition(name: SpringName, velocity?: number): SpringTransition {
  const { duration, bounce } = SPRINGS[name];
  const omega = (2 * Math.PI) / duration;
  const zeta = 1 - Math.min(Math.max(bounce, 0), 0.9);
  const base = { type: 'spring', stiffness: omega * omega, damping: 2 * zeta * omega, mass: 1, restDelta: 0.25, restSpeed: 5 } as const;
  return velocity === undefined || !Number.isFinite(velocity) ? base : { ...base, velocity };
}

/** `full` springs, `reduced` crossfades, `none` jumps. */
export type MotionPreference = 'full' | 'reduced' | 'none';

/**
 * The motion preference in effect: the `data-motion` attribute on `<html>`
 * (the app's in-app override, the same switch `tokens.css` reads), else the
 * OS `prefers-reduced-motion` setting.
 */
export function motionPreference(): MotionPreference {
  if (typeof document !== 'undefined') {
    const attr = document.documentElement.getAttribute('data-motion');
    if (attr === 'none' || attr === 'reduced' || attr === 'full') return attr;
  }
  if (typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches) return 'reduced';
  return 'full';
}
