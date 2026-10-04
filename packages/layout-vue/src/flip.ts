/**
 * FLIP for the board, on Motion springs.
 *
 * Each item element gets Motion values for its offset from its layout slot
 * (x, y), its scale, its opacity and, while a resize animates, its size. After
 * every layout change the controller measures each item's new slot, re-bases
 * the offset so the item stays where it visually is, and springs the offset
 * back to zero. Re-basing keeps the current velocity, so a reflow that
 * interrupts another reflow redirects smoothly instead of restarting.
 *
 * Slots are measured with `offsetLeft`/`offsetTop`, which ignore transforms,
 * so a measurement never sees an animation frame.
 */
import { animate, motionValue, type MotionValue } from 'motion-v';
import type { Point } from './momentum.ts';
import { FADE_DURATION, springTransition, type MotionPreference, type SpringName } from './springs.ts';

export interface Rect {
  readonly left: number;
  readonly top: number;
  readonly width: number;
  readonly height: number;
}

/** Scale of a lifted (dragged or picked-up) item. */
export const LIFT_SCALE = 1.03;
/** Scale of resting items in edit mode: they sit slightly back, ready to lift. */
export const EDIT_SCALE = 0.982;

class Entry {
  el: HTMLElement | null = null;
  slot: Rect | undefined;
  sizing = false;
  sizeToken = 0;
  readonly x: MotionValue<number> = motionValue(0);
  readonly y: MotionValue<number> = motionValue(0);
  readonly scale: MotionValue<number>;
  readonly opacity: MotionValue<number> = motionValue(1);
  readonly w: MotionValue<number> = motionValue(0);
  readonly h: MotionValue<number> = motionValue(0);
  readonly #off: VoidFunction[];

  constructor(scale: number) {
    this.scale = motionValue(scale);
    const write = (): void => this.write();
    this.#off = [this.x, this.y, this.scale, this.opacity, this.w, this.h].map((v) => v.on('change', write));
  }

  write(): void {
    const { el } = this;
    if (el === null) return;
    const x = this.x.get();
    const y = this.y.get();
    const s = this.scale.get();
    el.style.transform = x === 0 && y === 0 && s === 1 ? '' : `translate3d(${x}px, ${y}px, 0) scale(${s})`;
    const o = this.opacity.get();
    el.style.opacity = o >= 1 ? '' : String(o);
    if (this.sizing) {
      el.style.width = `${this.w.get()}px`;
      el.style.height = `${this.h.get()}px`;
    }
  }

  clearSize(): void {
    this.sizing = false;
    this.sizeToken++;
    this.w.stop();
    this.h.stop();
    if (this.el !== null) {
      this.el.style.width = '';
      this.el.style.height = '';
    }
  }

  destroy(): void {
    for (const off of this.#off) off();
    for (const v of [this.x, this.y, this.scale, this.opacity, this.w, this.h]) v.destroy();
  }
}

function measure(el: HTMLElement): Rect {
  return { left: el.offsetLeft, top: el.offsetTop, width: el.offsetWidth, height: el.offsetHeight };
}

const EPSILON = 0.5;

export interface PlayOptions {
  /** An item the pointer is holding: its slot is measured but its offset is left to `follow`. */
  readonly pinned?: string | undefined;
  /** An item being set down: it springs on `lift` with this initial velocity (px/s). */
  readonly settle?: { readonly id: string; readonly velocity: Point } | undefined;
}

export class ItemMotion {
  readonly #entries = new Map<string, Entry>();
  readonly #lifted = new Set<string>();
  readonly #preference: () => MotionPreference;
  #baseScale = 1;

  constructor(preference: () => MotionPreference) {
    this.#preference = preference;
  }

  #entry(id: string): Entry {
    let entry = this.#entries.get(id);
    if (entry === undefined) {
      entry = new Entry(this.#lifted.has(id) ? LIFT_SCALE : this.#baseScale);
      this.#entries.set(id, entry);
    }
    return entry;
  }

  /** Attach (or detach, with `null`) an item's element. Use as a function ref. */
  register(id: string, el: Element | null): void {
    const entry = this.#entry(id);
    const next = el instanceof HTMLElement ? el : null;
    if (entry.el === next) return;
    entry.el = next;
    entry.write();
  }

  /** The item's layout slot (px, relative to its offset parent) as of the last `play` or `sync`. */
  slot(id: string): Rect | undefined {
    return this.#entries.get(id)?.slot;
  }

  /** Re-measure every slot without animating (for container resizes). */
  sync(): void {
    for (const entry of this.#entries.values()) {
      if (entry.el === null) continue;
      if (entry.sizing) entry.clearSize();
      entry.slot = measure(entry.el);
    }
  }

  /**
   * Call after the layout changed and the DOM is patched: every item moves
   * from where it visually is to its new slot.
   */
  play(options: PlayOptions = {}): void {
    const preference = this.#preference();
    // Write phase: drop explicit sizes so the read phase sees slots.
    for (const entry of this.#entries.values()) {
      if (entry.el !== null && entry.sizing) {
        entry.el.style.width = '';
        entry.el.style.height = '';
      }
    }
    // Read phase.
    const slots = new Map<Entry, Rect>();
    for (const entry of this.#entries.values()) if (entry.el !== null) slots.set(entry, measure(entry.el));
    // Animate phase.
    for (const [id, entry] of this.#entries) {
      const to = slots.get(entry);
      if (to === undefined) {
        entry.destroy();
        this.#entries.delete(id);
        continue;
      }
      const from = entry.slot;
      entry.slot = to;
      if (id === options.pinned) {
        entry.write();
        continue;
      }
      const settle = options.settle?.id === id ? options.settle : undefined;
      if (from === undefined) {
        if (settle !== undefined) this.#translate(entry, entry.x.get(), entry.y.get(), 'lift', settle.velocity, preference);
        continue;
      }
      const dx = from.left - to.left;
      const dy = from.top - to.top;
      if (settle !== undefined || Math.abs(dx) >= EPSILON || Math.abs(dy) >= EPSILON) {
        this.#translate(entry, entry.x.get() + dx, entry.y.get() + dy, settle === undefined ? 'move' : 'lift', settle?.velocity, preference);
      }
      if (Math.abs(from.width - to.width) >= EPSILON || Math.abs(from.height - to.height) >= EPSILON) {
        this.#resize(entry, from, to, preference);
      } else {
        entry.write();
      }
    }
  }

  /** Hold an item at an offset from its slot (pointer drag). No spring. */
  follow(id: string, offset: Point): void {
    const entry = this.#entry(id);
    entry.x.jump(offset.x);
    entry.y.jump(offset.y);
  }

  /** Lift an item (scale up, on `lift`) or set it back down. */
  lift(id: string, lifted: boolean): void {
    if (lifted) this.#lifted.add(id);
    else this.#lifted.delete(id);
    this.#scaleTo(this.#entry(id), lifted ? LIFT_SCALE : this.#baseScale);
  }

  /** The scale resting items sit at (edit mode rests slightly smaller). */
  setBaseScale(scale: number): void {
    this.#baseScale = scale;
    for (const [id, entry] of this.#entries) if (!this.#lifted.has(id)) this.#scaleTo(entry, scale);
  }

  destroy(): void {
    for (const entry of this.#entries.values()) entry.destroy();
    this.#entries.clear();
  }

  #scaleTo(entry: Entry, scale: number): void {
    if (this.#preference() !== 'full') {
      entry.scale.jump(scale);
      return;
    }
    animate(entry.scale, scale, springTransition('lift'));
  }

  #translate(entry: Entry, x: number, y: number, spring: SpringName, velocity: Point | undefined, preference: MotionPreference): void {
    if (preference !== 'full') {
      const visible = Math.abs(x) >= EPSILON || Math.abs(y) >= EPSILON;
      entry.x.jump(0);
      entry.y.jump(0);
      if (preference === 'reduced' && visible) this.#crossfade(entry);
      return;
    }
    const vx = velocity?.x ?? entry.x.getVelocity();
    const vy = velocity?.y ?? entry.y.getVelocity();
    entry.x.jump(x);
    entry.y.jump(y);
    animate(entry.x, 0, springTransition(spring, vx));
    animate(entry.y, 0, springTransition(spring, vy));
  }

  #resize(entry: Entry, from: Rect, to: Rect, preference: MotionPreference): void {
    if (preference !== 'full') {
      entry.clearSize();
      if (preference === 'reduced') this.#crossfade(entry);
      entry.write();
      return;
    }
    const fromW = entry.sizing ? entry.w.get() : from.width;
    const fromH = entry.sizing ? entry.h.get() : from.height;
    const vw = entry.sizing ? entry.w.getVelocity() : 0;
    const vh = entry.sizing ? entry.h.getVelocity() : 0;
    const token = ++entry.sizeToken;
    entry.sizing = true;
    entry.w.jump(fromW);
    entry.h.jump(fromH);
    entry.write();
    const done = (): void => {
      if (entry.sizeToken === token) entry.clearSize();
    };
    Promise.all([
      animate(entry.w, to.width, springTransition('move', vw)),
      animate(entry.h, to.height, springTransition('move', vh)),
    ]).then(done, done);
  }

  #crossfade(entry: Entry): void {
    entry.opacity.jump(0.3);
    animate(entry.opacity, 1, { duration: FADE_DURATION, ease: 'easeOut' });
  }
}
