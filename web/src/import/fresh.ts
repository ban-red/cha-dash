/**
 * Tiles an import just added, marked "new" on the board for a few seconds
 * after landing (StaticBoard reads it). A ring, not motion, so reduced
 * motion changes nothing.
 */
import { reactive } from 'vue';

const fresh = reactive(new Set<string>());
let timer: ReturnType<typeof setTimeout> | undefined;

export function markFresh(ids: readonly string[], ms = 10_000): void {
  fresh.clear();
  for (const id of ids) fresh.add(id);
  if (timer) clearTimeout(timer);
  timer = setTimeout(() => fresh.clear(), ms);
}

export const isFresh = (id: string): boolean => fresh.has(id);

/** Files dropped on the page, waiting for the importer to take them; `n` counts drops. */
export const pending = reactive({ files: [] as File[], n: 0 });

export function hand(files: File[]): void {
  pending.files = files;
  pending.n++;
}

/** The pending files, once. */
export function takePending(): File[] {
  const files = pending.files;
  pending.files = [];
  return files;
}
