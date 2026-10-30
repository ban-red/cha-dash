/**
 * A widget's own settings (WidgetSpec.options), read from its tile config
 * with their defaults: a missing or malformed value is the default, so a
 * hand-edited or imported config never breaks a tile.
 */
import { computed, type ComputedRef } from 'vue';
import { WIDGETS, type WidgetOption } from '../model/widgets';
import { useTile } from '../stores/tiles';

export type OptionValue = boolean | string | string[];

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

/** The current tile's options for widget `type`, by key. */
export function useOptions(type: string): ComputedRef<Record<string, OptionValue>> {
  const tile = useTile();
  return computed(() => {
    const raw = tile.config.value?.options ?? {};
    return Object.fromEntries((WIDGETS[type]?.options ?? []).map((o) => [o.key, optionValue(o, raw[o.key])]));
  });
}
