/**
 * The last-known snapshot, kept in IndexedDB so the page can show what it
 * last saw when the hub can't be reached (ADR-0011): the three topics, the
 * board's layout, who was signed in, and when.
 *
 * - One record, replaced on every save. Stored as a JSON string: the topic
 *   documents are Vue proxies, which IndexedDB can't clone.
 * - Every call swallows its errors and resolves (private windows, blocked
 *   storage, quota): offline support is a nicety, never a reason to fail.
 * - Signing out deletes it. Nothing here comes from an HTTP cache; the service
 *   worker never stores /api responses.
 */
import type { GraphDoc, InboxDoc, MetricsDoc } from '../model/types';

export const SNAPSHOT_VERSION = 1;
const DB = 'chadash';
const STORE = 'snapshot';
const KEY = 'last';

export interface SnapshotBoard {
  id: string;
  name: string;
  revision: number;
  layout: unknown;
}

export interface Snapshot {
  v: typeof SNAPSHOT_VERSION;
  /** When it was taken (ms epoch, browser clock). */
  savedAt: number;
  user: { id: string; username: string; role: string };
  graph?: GraphDoc;
  metrics?: MetricsDoc;
  inbox?: InboxDoc;
  board?: SnapshotBoard;
  /** The display time zone in force (demo mode uses UTC). */
  timeZone?: string;
  /** Hub clock minus browser clock when it was taken (the Demo Home runs its own clock). */
  clockOffset?: number;
  /** The hub was in demo mode. */
  demo?: boolean;
}

function idb(): IDBFactory | undefined {
  try {
    return typeof indexedDB === 'undefined' ? undefined : indexedDB;
  } catch {
    return undefined;
  }
}

function open(timeoutMs = 2000): Promise<IDBDatabase | undefined> {
  const factory = idb();
  if (!factory) return Promise.resolve(undefined);
  return new Promise((resolve) => {
    let done = false;
    const finish = (db: IDBDatabase | undefined) => {
      if (done) {
        db?.close();
        return;
      }
      done = true;
      clearTimeout(timer);
      resolve(db);
    };
    // An open can hang when another tab holds an old version; don't wait forever.
    const timer = setTimeout(() => finish(undefined), timeoutMs);
    try {
      const req = factory.open(DB, 1);
      req.onupgradeneeded = () => {
        if (!req.result.objectStoreNames.contains(STORE)) req.result.createObjectStore(STORE);
      };
      req.onsuccess = () => finish(req.result);
      req.onerror = () => finish(undefined);
      req.onblocked = () => finish(undefined);
    } catch {
      finish(undefined);
    }
  });
}

function run<T>(mode: IDBTransactionMode, fn: (store: IDBObjectStore) => IDBRequest<T>): Promise<T | undefined> {
  return open().then(
    (db) =>
      new Promise<T | undefined>((resolve) => {
        if (!db) return resolve(undefined);
        try {
          const tx = db.transaction(STORE, mode);
          const req = fn(tx.objectStore(STORE));
          let result: T | undefined;
          req.onsuccess = () => (result = req.result);
          tx.oncomplete = () => {
            db.close();
            resolve(result);
          };
          tx.onerror = tx.onabort = () => {
            db.close();
            resolve(undefined);
          };
        } catch {
          db.close();
          resolve(undefined);
        }
      }),
  );
}

/** Store the snapshot. Resolves false when storage isn't available. */
export async function saveSnapshot(s: Omit<Snapshot, 'v'>): Promise<boolean> {
  let text: string;
  try {
    text = JSON.stringify({ ...s, v: SNAPSHOT_VERSION });
  } catch {
    return false;
  }
  const ok = await run('readwrite', (store) => store.put(text, KEY));
  return ok !== undefined;
}

/** The last snapshot, or undefined (none, unreadable, or from another version). */
export async function loadSnapshot(): Promise<Snapshot | undefined> {
  const text = await run<unknown>('readonly', (store) => store.get(KEY));
  if (typeof text !== 'string') return undefined;
  try {
    const s = JSON.parse(text) as Snapshot;
    if (s?.v !== SNAPSHOT_VERSION || typeof s.savedAt !== 'number' || !s.user?.id) return undefined;
    return s;
  } catch {
    return undefined;
  }
}

export async function clearSnapshot(): Promise<void> {
  await run('readwrite', (store) => store.delete(KEY));
}
