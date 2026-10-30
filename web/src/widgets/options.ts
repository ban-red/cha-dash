/**
 * A widget's own settings (WidgetSpec.options), read from its tile config
 * with their defaults: a missing or malformed value is the default, so a
 * hand-edited or imported config never breaks a tile.
 */
import { computed, type ComputedRef } from 'vue';
import { WIDGETS, type WidgetOption } from '../model/widgets';
import { useTile } from '../stores/tiles';

export type OptionValue = boolean | string | string[] | Record<string, string>;

/** One option's value from a tile's `options`, else its default. */
export function optionValue(opt: WidgetOption, raw: unknown): OptionValue {
  switch (opt.kind) {
    case 'boolean':
      return typeof raw === 'boolean' ? raw : opt.default;
    case 'choice':
      return typeof raw === 'string' && opt.choices.some((c) => c.value === raw) ? raw : opt.default;
    case 'resources':
      return Array.isArray(raw) ? raw.filter((x): x is string => typeof x === 'string') : [];
  }
}

/** A `resources` option's sizes by resource id, only the valid non-default ones. */
export function sizesValue(opt: WidgetOption, raw: unknown): Record<string, string> {
  if (opt.kind !== 'resources' || !opt.sizes || typeof raw !== 'object' || raw === null || Array.isArray(raw)) return {};
  const ok = new Set(opt.sizes.choices.map((c) => c.value));
  return Object.fromEntries(Object.entries(raw).filter((e): e is [string, string] => typeof e[1] === 'string' && ok.has(e[1]) && e[1] !== opt.sizes!.default));
}

/** The current tile's options for widget `type`, by key; a `resources` option's sizes under their own key. */
export function useOptions(type: string): ComputedRef<Record<string, OptionValue>> {
  const tile = useTile();
  return computed(() => {
    const raw = tile.config.value?.options ?? {};
    return Object.fromEntries(
      (WIDGETS[type]?.options ?? []).flatMap((o): [string, OptionValue][] => [
        [o.key, optionValue(o, raw[o.key])],
        ...(o.kind === 'resources' && o.sizes ? [[o.sizes.key, sizesValue(o, raw[o.sizes.key])] as [string, OptionValue]] : []),
      ]),
    );
  });
}
