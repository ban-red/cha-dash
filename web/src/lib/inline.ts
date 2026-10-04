/**
 * The first-paint snapshot the hub inlines into index.html for a signed-in
 * owner (internal/firstpaint): the first board and the graph, metrics and
 * inbox as the socket would send them, trimmed to what the front page shows.
 *
 *   <script type="application/json" id="chadash-snapshot">{…}</script>
 *
 * A data block, so it never executes and the CSP stays strict: it's read as
 * text and parsed as JSON, never evaluated. It's taken once and removed from
 * the page, so a later sign-in in the same tab can't pick up an old one.
 * It's current, not last-known: the page renders from it at once (no
 * skeletons) and the socket's first snapshots supersede it. When it's
 * missing (signed out, the generic shell from the service worker, a hub
 * without it), the page starts the usual way.
 */
import type { GraphDoc, InboxDoc, MetricsDoc } from '../model/types';

export const INLINE_SNAPSHOT_ID = 'chadash-snapshot';
export const INLINE_SNAPSHOT_VERSION = 1;

export interface InlineBoard {
  id: string;
  name: string;
  revision: number;
  format_version: number;
  /** null until the board's first save. */
  layout: unknown;
}

export interface InlineSnapshot {
  v: typeof INLINE_SNAPSHOT_VERSION;
  user: { id: string; username: string; role: string };
  boards: { id: string; name: string }[];
  board: InlineBoard | null;
  /** The Demo Home: UTC display times and the simulator's clock. */
  demo?: boolean;
  graph: GraphDoc;
  metrics: MetricsDoc;
  inbox: InboxDoc;
}

/** Read and remove the snapshot, or undefined when there's none or it can't be trusted. */
export function takeInlineSnapshot(doc: Document | undefined = typeof document === 'undefined' ? undefined : document): InlineSnapshot | undefined {
  const el = doc?.getElementById(INLINE_SNAPSHOT_ID);
  if (!el) return undefined;
  el.remove();
  if (el.tagName !== 'SCRIPT' || el.getAttribute('type') !== 'application/json') return undefined;
  try {
    const s = JSON.parse(el.textContent ?? '') as Partial<InlineSnapshot> | null;
    if (
      s?.v !== INLINE_SNAPSHOT_VERSION ||
      typeof s.user?.id !== 'string' ||
      !s.user.id ||
      typeof s.user.username !== 'string' ||
      !s.graph?.resources ||
      !Array.isArray(s.boards)
    ) {
      return undefined;
    }
    return s as InlineSnapshot;
  } catch {
    return undefined;
  }
}
