/**
 * Keeps the last-known snapshot fresh while the page is live: at most every
 * 15 s while frames arrive, and straight away when the tab is hidden (the
 * moment a phone is likely to lose the hub). Never while showing a restored
 * snapshot: that would only re-store old data with a new time.
 */
import { watch } from 'vue';
import { getDisplayTimeZone } from '../lib/format';
import { saveSnapshot } from '../lib/snapshot';
import type { BoardStore } from './board';
import type { Live } from './live';
import type { Session } from './session';

export function persistSnapshots(live: Live, board: BoardStore, session: Session, everyMs = 15_000): () => void {
  let last = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;

  async function save(): Promise<void> {
    timer = undefined;
    const user = session.state.user;
    if (live.conn.status !== 'live' || !live.doc.graph || !user || session.state.offline) return;
    last = Date.now();
    const b = board.snapshot();
    const tz = getDisplayTimeZone();
    await saveSnapshot({
      savedAt: last,
      user: { id: user.id, username: user.username, role: user.role },
      graph: live.doc.graph,
      ...(live.doc.metrics ? { metrics: live.doc.metrics } : {}),
      ...(live.doc.inbox ? { inbox: live.doc.inbox } : {}),
      ...(b ? { board: b } : {}),
      ...(tz ? { timeZone: tz } : {}),
      clockOffset: live.clockOffset(),
      demo: live.demo.value,
    });
  }

  function schedule(): void {
    if (timer !== undefined) return;
    timer = setTimeout(() => void save(), Math.max(0, last + everyMs - Date.now()));
  }

  function now(): void {
    clearTimeout(timer);
    void save();
  }
  function flush(): void {
    if (document.visibilityState === 'hidden') now();
  }

  const stopWatch = watch(() => live.conn.lastFrameAt, schedule);
  const stopBoard = watch(() => board.state.revision, schedule);
  document.addEventListener('visibilitychange', flush);
  window.addEventListener('pagehide', now);

  return () => {
    stopWatch();
    stopBoard();
    clearTimeout(timer);
    timer = undefined;
    document.removeEventListener('visibilitychange', flush);
    window.removeEventListener('pagehide', now);
  };
}
