/**
 * Levels of detail (02 · Experience, "Levels of detail"; plan 15, wave 34).
 * Every widget renders at four levels, chosen by the surface it's on rather
 * than by the widget:
 *
 *   full        the desk: everything
 *   compact     the phone: the primary value, its context and one action
 *   simplified  a wall or a TV: bigger type, no controls, the one number
 *   ink         e-ink and print: one colour, no motion, absolute times
 *
 * The board surface provides the level (StaticBoard: compact at the phone
 * breakpoint, else full; DisplayPage: simplified, or ink for an e-ink
 * display). Tile.vue marks itself with it (`lvl-<level>`), and the shared
 * tile styles (styles/tiles.css, "Levels") do the rest from the widgets'
 * common parts (`.v` the value, `.sub` and `.kv` the secondary facts, `.tf`
 * the foot, `.list` the rows), so a widget gets every level from the parts
 * it's already built of. Times read the level too: at ink, "4m ago" is
 * "at 14:05" (format.ts → ago, since).
 */
import { computed, hasInjectionContext, inject, provide, type ComputedRef, type InjectionKey, type MaybeRefOrGetter, toValue } from 'vue';

export type Level = 'full' | 'compact' | 'simplified' | 'ink';

export const LEVELS: readonly Level[] = ['full', 'compact', 'simplified', 'ink'];

const KEY: InjectionKey<ComputedRef<Level>> = Symbol('chadash.level');

/** Provides the level to everything below: a value, a ref or a getter. */
export function provideLevel(level: MaybeRefOrGetter<Level>): ComputedRef<Level> {
  const c = computed(() => toValue(level));
  provide(KEY, c);
  return c;
}

/** The level here; full where nothing provides one. */
export function useLevel(): ComputedRef<Level> {
  return inject(KEY, null) ?? computed(() => 'full' as Level);
}

/**
 * The level of the component being set up or rendered, for plain
 * functions that format (format.ts): full outside a component.
 */
export function currentLevel(): Level {
  if (!hasInjectionContext()) return 'full';
  return inject(KEY, null)?.value ?? 'full';
}
