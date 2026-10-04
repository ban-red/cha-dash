/**
 * Momentum projection for drops: where would the widget come to rest if it
 * kept the velocity it had when the pointer let go?
 *
 * The projection uses the formula from Apple's "Designing Fluid Interfaces"
 * (WWDC18): a body decelerating by a factor `r` per millisecond travels
 * `v / 1000 * r / (1 - r)` further. A dead zone keeps slow, deliberate drops
 * exactly where the pointer is, and a cap keeps a hard flick on the board.
 */

export interface Point {
  readonly x: number;
  readonly y: number;
}

export interface MomentumOptions {
  /**
   * Deceleration per millisecond. 0.995 projects 0.2 s of travel: halfway
   * between UIKit's `.fast` (0.99, 0.1 s) and `.normal` (0.998, 0.5 s), and
   * the same 0.2 s the prototype uses to dismiss sheets with momentum.
   */
  readonly decelerationRate: number;
  /** Pointer history used to measure the release velocity, in ms. */
  readonly velocityWindow: number;
  /** Speeds up to this (px/s) don't project. Faster releases project only the excess. */
  readonly minSpeed: number;
  /** The projection never exceeds this many grid cells (one Large widget). */
  readonly maxCells: number;
  /** Pointer travel (px) before a press becomes a drag. */
  readonly dragThreshold: number;
}

export const MOMENTUM: MomentumOptions = Object.freeze({
  decelerationRate: 0.995,
  velocityWindow: 100,
  minSpeed: 250,
  maxCells: 4,
  dragThreshold: 4,
});

/** Seconds of travel a decelerating body projects: `r / (1 - r) / 1000`. */
export function projectionTime(decelerationRate: number = MOMENTUM.decelerationRate): number {
  return decelerationRate / (1 - decelerationRate) / 1000;
}

const ZERO: Point = Object.freeze({ x: 0, y: 0 });

/**
 * The displacement (px) a release velocity (px/s) projects, given the grid
 * pitch (cell plus gutter, px) for the cap.
 */
export function projectMomentum(velocity: Point, pitch: number, options: MomentumOptions = MOMENTUM): Point {
  const speed = Math.hypot(velocity.x, velocity.y);
  if (!Number.isFinite(speed) || speed <= options.minSpeed) return ZERO;
  const k = ((speed - options.minSpeed) / speed) * projectionTime(options.decelerationRate);
  let x = velocity.x * k;
  let y = velocity.y * k;
  const max = options.maxCells * Math.max(0, pitch);
  const distance = Math.hypot(x, y);
  if (distance > max) {
    const scale = max / distance;
    x *= scale;
    y *= scale;
  }
  return { x, y };
}

interface Sample {
  readonly t: number;
  readonly x: number;
  readonly y: number;
}

/**
 * Release velocity from recent pointer samples: a least-squares slope over
 * the last `window` ms, which is steadier than first-to-last on jittery input.
 * A pointer that rested before letting go has no samples in the window, so
 * its velocity is zero.
 */
export class VelocityTracker {
  readonly #window: number;
  #samples: Sample[] = [];

  constructor(window: number = MOMENTUM.velocityWindow) {
    this.#window = window;
  }

  add(t: number, x: number, y: number): void {
    const last = this.#samples[this.#samples.length - 1];
    if (last !== undefined && t < last.t) this.#samples = [];
    this.#samples.push({ t, x, y });
    const cutoff = t - this.#window * 2;
    while (this.#samples.length > 2 && (this.#samples[0] as Sample).t < cutoff) this.#samples.shift();
  }

  reset(): void {
    this.#samples = [];
  }

  /** Velocity in px/s at time `now` (ms). */
  velocity(now: number): Point {
    const recent = this.#samples.filter((s) => s.t >= now - this.#window && s.t <= now);
    if (recent.length < 2) return ZERO;
    const n = recent.length;
    let mt = 0;
    let mx = 0;
    let my = 0;
    for (const s of recent) {
      mt += s.t / n;
      mx += s.x / n;
      my += s.y / n;
    }
    let tt = 0;
    let tx = 0;
    let ty = 0;
    for (const s of recent) {
      const dt = s.t - mt;
      tt += dt * dt;
      tx += dt * (s.x - mx);
      ty += dt * (s.y - my);
    }
    if (tt < 1e-6) return ZERO;
    return { x: (tx / tt) * 1000, y: (ty / tt) * 1000 };
  }
}
