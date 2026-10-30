/**
 * Tile views: a widget that has more to show than its size has room for
 * offers it as views, and the tile pages between them (Tile.vue: swipe on a
 * touch screen, the dots below, or the arrow keys on the dots). Which views a
 * tile has is the widget's call, by size and by what its target has: a host
 * without guests has no Guests view. With fewer than two there's nothing to
 * page, and the tile looks as it always did.
 *
 * The view shown is remembered per tile on this device (localStorage), the
 * way the theme is. A remembered view the tile no longer offers falls back
 * to the first.
 */
import { computed, reactive, type ComputedRef } from 'vue';
import { useTile } from '../stores/tiles';

export interface TileView {
  id: string;
  /** Its name, for the dot's label and the tile's ("Hourly"). */
  label: string;
}

/** What Tile.vue needs to page: the views, the one shown, and how to pick another. */
export interface TileViews {
  list: readonly TileView[];
  current: string;
  select(id: string): void;
}

export const VIEWS_KEY = 'chadash.views';

function readStored(): Record<string, string> {
  try {
    const v: unknown = JSON.parse(globalThis.localStorage?.getItem(VIEWS_KEY) ?? '{}');
    if (typeof v !== 'object' || v === null || Array.isArray(v)) return {};
    return Object.fromEntries(Object.entries(v).filter((e): e is [string, string] => typeof e[1] === 'string'));
  } catch {
    return {};
  }
}

/** The view picked per layout item id, shared by every tile on the page. */
const chosen = reactive<Record<string, string>>(readStored());

function store(): void {
  try {
    globalThis.localStorage?.setItem(VIEWS_KEY, JSON.stringify(chosen));
  } catch {
    /* private mode or blocked storage: the choice lasts for this page only */
  }
}

/** Forget every remembered view (tests). */
export function resetViews(): void {
  for (const k of Object.keys(chosen)) delete chosen[k];
}

/**
 * The views a widget offers, and the one to render. `views` goes to <Frame>
 * (or <Tile>); `view` is what the template switches on. Outside a board (the
 * widget picker's preview) the pick lasts as long as the tile.
 */
export function useViews(offer: () => readonly TileView[]): { views: ComputedRef<TileViews | undefined>; view: ComputedRef<string> } {
  const tile = useTile();
  const local = reactive<{ id?: string }>({});
  const list = computed(() => offer());
  const view = computed(() => {
    const l = list.value;
    const want = tile.itemId ? chosen[tile.itemId] : local.id;
    return l.find((v) => v.id === want)?.id ?? l[0]?.id ?? '';
  });
  const views = computed<TileViews | undefined>(() =>
    list.value.length < 2
      ? undefined
      : {
          list: list.value,
          current: view.value,
          select(id) {
            if (!list.value.some((v) => v.id === id)) return;
            if (!tile.itemId) {
              local.id = id;
              return;
            }
            chosen[tile.itemId] = id;
            store();
          },
        },
  );
  return { views, view };
}
