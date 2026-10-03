/**
 * Immutable undo/redo history. Generic, but meant for `Board`: boards are
 * immutable and structurally shared, so each entry costs only the sections
 * that changed.
 */
export interface History<T> {
  /** Older states, oldest first. At most `limit` entries. */
  readonly past: readonly T[];
  readonly present: T;
  /** States undone, next redo first. */
  readonly future: readonly T[];
  /** Maximum number of undo steps kept. */
  readonly limit: number;
}

export const DEFAULT_HISTORY_LIMIT = 100;

export function createHistory<T>(present: T, options: { readonly limit?: number } = {}): History<T> {
  const limit = options.limit ?? DEFAULT_HISTORY_LIMIT;
  if (!Number.isInteger(limit) || limit < 1) {
    throw new RangeError(`history limit must be a positive integer, got ${limit}`);
  }
  return { past: [], present, future: [], limit };
}

/**
 * Record a new present state. Clears the redo stack and drops the oldest
 * entry past the limit. Committing the current present (same reference,
 * e.g. an operation that changed nothing) returns the history unchanged.
 */
export function commit<T>(history: History<T>, next: T): History<T> {
  if (Object.is(next, history.present)) return history;
  const past = [...history.past, history.present];
  if (past.length > history.limit) past.splice(0, past.length - history.limit);
  return { past, present: next, future: [], limit: history.limit };
}

/** Step back. Returns the same history when there is nothing to undo. */
export function undo<T>(history: History<T>): History<T> {
  const { past } = history;
  if (past.length === 0) return history;
  return {
    past: past.slice(0, -1),
    present: past[past.length - 1] as T,
    future: [history.present, ...history.future],
    limit: history.limit,
  };
}

/** Step forward. Returns the same history when there is nothing to redo. */
export function redo<T>(history: History<T>): History<T> {
  const [next, ...future] = history.future;
  if (history.future.length === 0) return history;
  return { past: [...history.past, history.present], present: next as T, future, limit: history.limit };
}

export function canUndo(history: History<unknown>): boolean {
  return history.past.length > 0;
}

export function canRedo(history: History<unknown>): boolean {
  return history.future.length > 0;
}
