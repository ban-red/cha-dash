/**
 * Reconnect delays: exponential with "equal jitter" (half fixed, half random),
 * so a hub restart doesn't get every tab back in the same instant, and no
 * retry is ever immediate.
 *
 *   attempt 0 → 0.5–1 s, 1 → 1–2 s, 2 → 2–4 s … capped at 15–30 s.
 */
export interface BackoffOptions {
  /** First delay ceiling in ms. */
  base?: number;
  /** Delay ceiling in ms. */
  max?: number;
  factor?: number;
  random?: () => number;
}

export function backoffDelay(attempt: number, opts: BackoffOptions = {}): number {
  const { base = 1000, max = 30_000, factor = 2, random = Math.random } = opts;
  const ceiling = Math.min(max, base * factor ** Math.max(0, attempt));
  return Math.round(ceiling / 2 + random() * (ceiling / 2));
}
