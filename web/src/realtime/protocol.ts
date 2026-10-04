/**
 * Realtime protocol v1 frames (see internal/realtime/protocol.go).
 *
 *   → hello {v:1, resume?}         first frame, within 10 s of opening
 *   ← hello {v, epoch, server, ping}
 *   → sub {id, topics}             ← snap {id, seq, data}
 *   ← diff {id, seq, patch}        RFC 6902, paths prefixed with the topic
 *   ← snap {id, seq, data, resync: true}   after lag: replace state
 *   ← ping                          every `ping` seconds
 *   → ping                          ← pong
 *   → unsub {id}
 *   ← error {id?, code, msg}
 */
import type { PatchOp } from '../lib/jsonpatch';

export const PROTOCOL_VERSION = 1;

export type ClientFrame =
  | { op: 'hello'; v: number; resume?: number }
  | { op: 'sub'; id: number; topics: string[] }
  | { op: 'unsub'; id: number }
  | { op: 'ping' }
  | { op: 'pong' };

export interface HelloFrame {
  op: 'hello';
  v: number;
  epoch: string;
  server?: string;
  /** Server ping interval in seconds. */
  ping: number;
}

export interface SnapFrame {
  op: 'snap';
  id: number;
  seq: number;
  data: Record<string, unknown>;
  resync?: boolean;
}

export interface DiffFrame {
  op: 'diff';
  id: number;
  seq: number;
  patch: PatchOp[];
}

export interface ErrorFrame {
  op: 'error';
  id?: number;
  code: string;
  msg: string;
}

export type ServerFrame = HelloFrame | SnapFrame | DiffFrame | ErrorFrame | { op: 'ping' } | { op: 'pong' };

export const ERROR_CODES = {
  badRequest: 'bad_request',
  version: 'unsupported_version',
  unknownOp: 'unknown_op',
  unknownTopic: 'unknown_topic',
  duplicateId: 'duplicate_id',
  unknownSub: 'unknown_sub',
  tooManySubs: 'too_many_subs',
} as const;

/** Parse one text frame. Returns undefined for anything that isn't a known frame. */
export function parseFrame(text: string): ServerFrame | undefined {
  let v: unknown;
  try {
    v = JSON.parse(text);
  } catch {
    return undefined;
  }
  if (typeof v !== 'object' || v === null) return undefined;
  const op = (v as { op?: unknown }).op;
  switch (op) {
    case 'hello':
    case 'snap':
    case 'diff':
    case 'error':
    case 'ping':
    case 'pong':
      return v as ServerFrame;
    default:
      return undefined;
  }
}
