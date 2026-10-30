/**
 * Warnings someone hid on this device. A hidden warning stays out of Needs
 * attention and the home briefing for as long as it is only a warning
 * (severity `attention`); once it turns critical (`down`) it shows again,
 * and it goes back to hidden if it eases to a warning. The tile and the
 * Inbox still show it, marked hidden, with "Show again".
 *
 * Kept in localStorage like the tile views: a per-device convenience, not
 * something the hub or other viewers see. The key is the kind of problem
 * and what it's about, so a warning that resolves and returns stays hidden.
 */
import { reactive } from 'vue';
import type { Incident } from '../model/types';

export const MUTED_KEY = 'chadash.muted';

function read(): Set<string> {
  try {
    const v: unknown = JSON.parse(globalThis.localStorage?.getItem(MUTED_KEY) ?? '[]');
    return new Set(Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : []);
  } catch {
    return new Set();
  }
}

const state = reactive({ keys: read() });

function save(): void {
  try {
    globalThis.localStorage?.setItem(MUTED_KEY, JSON.stringify([...state.keys]));
  } catch {
    /* private window or blocked storage: it holds until the page closes */
  }
}

export const muteKey = (i: Incident) => `${i.scenario ?? i.title}|${i.target}`;

/** A warning can be hidden; a down (critical) problem, a repair and an approval can't. */
export const canMute = (i: Incident) => i.class === 'problem' && i.severity === 'attention';

/** Hidden now: marked hidden, and still only a warning. */
export const isMuted = (i: Incident) => canMute(i) && state.keys.has(muteKey(i));

/** Marked hidden, whatever its severity is at the moment. */
export const isMarked = (i: Incident) => state.keys.has(muteKey(i));

export function mute(i: Incident): void {
  state.keys.add(muteKey(i));
  save();
}

export function unmute(i: Incident): void {
  state.keys.delete(muteKey(i));
  save();
}

/** For tests: forget what was hidden and read storage again. */
export function resetMuted(): void {
  state.keys = read();
}
