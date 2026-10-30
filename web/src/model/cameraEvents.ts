import { attr } from './graph';
import type { Resource } from './types';

/** One detection a camera reported. */
export interface CameraEvent {
  /** Frigate's id; empty when the source gave none (the Demo Home). */
  id: string;
  label: string;
  at: string;
  score: number | undefined;
  /** The event's page in Frigate, when the hub has one. */
  url: string | undefined;
}

/**
 * A camera's newest events, newest first: the hub's `events.<n>.*` (up to
 * five), or, from a source that only has the one, its `last_event.*`.
 */
export function cameraEvents(c: Resource | undefined): CameraEvent[] {
  if (!c) return [];
  const out: CameraEvent[] = [];
  for (let i = 0; i < 20; i++) {
    const at = attr<string>(c, `events.${i}.at`);
    if (!at) break;
    out.push({
      id: attr<string>(c, `events.${i}.id`) ?? '',
      label: attr<string>(c, `events.${i}.label`) ?? '',
      at,
      score: attr<number>(c, `events.${i}.score`),
      url: attr<string>(c, `events.${i}.url`),
    });
  }
  if (out.length) return out;
  const at = attr<string>(c, 'last_event.at');
  return at
    ? [{ id: attr<string>(c, 'last_event.id') ?? '', label: attr<string>(c, 'last_event.label') ?? '', at, score: attr<number>(c, 'last_event.score'), url: attr<string>(c, 'last_event.url') }]
    : [];
}
