/**
 * Label discovery for the board on screen: which containers' `homepage.*`
 * labels suggest tiles (model/discovery.ts), minus the ones dismissed on this
 * board, which the hub remembers:
 *
 *   GET  /api/boards/{id}/discovery          → {"dismissed": ["<resource id>", …]}
 *   POST /api/boards/{id}/discovery/dismiss  {"resources": [...]} → {"dismissed": [...]}
 *
 * The dismissed list is fetched only once there is something to suggest, so
 * a board with nothing to discover costs no request. Nothing is suggested
 * until it has arrived (a dismissed suggestion never flashes back), while
 * the hub is unreachable, or on a board that isn't saved.
 */
import { computed, inject, provide, shallowRef, watch, type InjectionKey } from 'vue';
import { api } from '../lib/api';
import { addLabelServices, labelServices, type LabelService } from '../model/discovery';
import type { BoardStore } from './board';
import type { Live } from './live';

const NONE: ReadonlySet<string> = new Set();

export function createDiscovery(live: Live, board: BoardStore) {
  const dismissed = shallowRef<ReadonlySet<string>>(NONE);
  /** The board whose dismissals are loaded ('' for none yet). */
  const loadedFor = shallowRef('');
  let loading = '';

  const usable = computed(() => {
    const s = board.state;
    return !live.stale.value && !s.lastKnown && !s.unsaved && s.status === 'ready' && Boolean(s.boardId);
  });
  /** Anything to ask about, dismissals aside: the cue to fetch them. */
  const candidates = computed(() => usable.value && labelServices(live.graph.value, board.board.value, board.tiles.value, NONE).length > 0);

  /** What the Inbox offers: suggestions not dismissed, once the dismissals are known. */
  const suggestions = computed<LabelService[]>(() => {
    if (!usable.value || !loadedFor.value || loadedFor.value !== board.state.boardId) return [];
    return labelServices(live.graph.value, board.board.value, board.tiles.value, dismissed.value);
  });

  async function load(id: string): Promise<void> {
    if (loading === id) return;
    loading = id;
    try {
      const res = await api<{ dismissed?: unknown }>(`/api/boards/${encodeURIComponent(id)}/discovery`);
      if (board.state.boardId !== id) return;
      dismissed.value = new Set(Array.isArray(res?.dismissed) ? res.dismissed.filter((x): x is string => typeof x === 'string') : []);
      loadedFor.value = id;
    } catch {
      /* offline, or an older hub: suggest nothing rather than something dismissed */
    } finally {
      if (loading === id) loading = '';
    }
  }

  watch(
    () => [candidates.value, board.state.boardId] as const,
    ([any, id]) => {
      if (any && id && loadedFor.value !== id) void load(id);
    },
    { immediate: true },
  );

  /**
   * Add every suggestion to the board in one save. Resolves with what was
   * added, or null on a conflict (the other writer's board is loaded instead).
   */
  async function add(): Promise<LabelService[] | null> {
    const list = suggestions.value;
    if (!list.length || !board.board.value) return [];
    const next = addLabelServices({ board: board.board.value, tiles: board.tiles.value }, list);
    if (!next.added.length) return [];
    const saved = await board.save(next.board, next.tiles);
    return saved ? next.added : null;
  }

  /**
   * Remember that these resources aren't wanted on this board (the Inbox's
   * Dismiss, or removing a discovered tile). They stop being suggested at
   * once; rejects if the hub couldn't store it.
   */
  async function remember(resources: readonly string[]): Promise<void> {
    const id = board.state.boardId;
    if (!resources.length || !id) return;
    dismissed.value = new Set([...dismissed.value, ...resources]);
    const res = await api<{ dismissed?: unknown }>(`/api/boards/${encodeURIComponent(id)}/discovery/dismiss`, {
      method: 'POST',
      body: { resources: [...resources] },
    });
    if (board.state.boardId === id && Array.isArray(res?.dismissed)) {
      dismissed.value = new Set([...dismissed.value, ...res.dismissed.filter((x): x is string => typeof x === 'string')]);
    }
  }

  /** Dismiss what's suggested now (and the same services seen by other sources). */
  function dismiss(): Promise<void> {
    return remember(suggestions.value.flatMap((s) => [s.resource, ...s.duplicates]));
  }

  return { suggestions, add, dismiss, remember };
}

export type Discovery = ReturnType<typeof createDiscovery>;

const KEY: InjectionKey<Discovery> = Symbol('discovery');
export const provideDiscovery = (d: Discovery) => provide(KEY, d);
export function useDiscovery(): Discovery {
  const d = inject(KEY);
  if (!d) throw new Error('useDiscovery() outside the app');
  return d;
}
/** For chrome that also renders without a board (the Briefing). */
export const useDiscoveryIfAny = (): Discovery | undefined => inject(KEY, undefined);
