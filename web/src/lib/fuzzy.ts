/**
 * Fuzzy matching for the command bar. Small and predictable rather than
 * clever: every word of the query has to match somewhere, and a match ranks by
 * how it matched.
 *
 *   exact 100 · prefix 80 · start of a word 60 · substring 40 ·
 *   letters in order (mgp → magpie) 1–20, fewer gaps first
 *
 * Fields are weighted: the label counts fully, secondary text (kind, id,
 * keywords) at 0.6, so "pbs" ranks the PBS host above things that merely
 * mention it.
 */

const norm = (s: string) => s.toLowerCase().normalize('NFKD').replace(/[̀-ͯ]/g, '');

const WORD_BREAK = /[\s/:._@#-]/;

/** How well one query word matches one text, 0 when it doesn't. */
export function matchWord(word: string, text: string): number {
  if (!word) return 1;
  if (!text) return 0;
  if (text === word) return 100;
  if (text.startsWith(word)) return 80;
  let at = text.indexOf(word);
  let best = 0;
  while (at >= 0) {
    best = Math.max(best, at === 0 || WORD_BREAK.test(text[at - 1]!) ? 60 : 40);
    if (best === 60) break;
    at = text.indexOf(word, at + 1);
  }
  if (best) return best;
  // Letters in order, anywhere. Penalise the gaps between them.
  let j = 0;
  let gaps = 0;
  let last = -1;
  for (const c of word) {
    const k = text.indexOf(c, j);
    if (k < 0) return 0;
    if (last >= 0 && k > last + 1) gaps += k - last - 1;
    last = k;
    j = k + 1;
  }
  return Math.max(1, 20 - gaps);
}

export interface Searchable {
  label: string;
  /** Secondary text matched at a lower weight: kind, id, aliases. */
  keywords?: string;
}

/** Score an item for a query; 0 means it doesn't match. An empty query matches everything with 1. */
export function score(query: string, item: Searchable): number {
  const words = norm(query).split(/\s+/).filter(Boolean);
  if (!words.length) return 1;
  const label = norm(item.label);
  const extra = item.keywords ? norm(item.keywords) : '';
  let total = 0;
  for (const w of words) {
    const s = Math.max(matchWord(w, label), matchWord(w, extra) * 0.6);
    if (!s) return 0;
    total += s;
  }
  // A whole-query hit on the label beats the same words scattered.
  const whole = norm(query).trim();
  if (words.length > 1 && label.includes(whole)) total += 30;
  return total;
}

/** Items that match, best first; ties keep their input order. */
export function rank<T extends Searchable>(query: string, items: readonly T[]): T[] {
  return items
    .map((item, i) => ({ item, i, s: score(query, item) }))
    .filter((x) => x.s > 0)
    .sort((a, b) => b.s - a.s || a.i - b.i)
    .map((x) => x.item);
}
