/**
 * Guests (VMs and LXCs) as rows, from what Proxmox VE publishes
 * (integrations/proxmox): state, `onboot`, `lock`, `started_at`,
 * `backup.last_ok`/`backup.last_status`, and `cpu.pct`/`mem.pct` while running.
 */
import type { TileStatus } from '../components/Tile.vue';
import { ago, duration, since } from '../lib/format';
import { attr, label, outgoing, series, value } from '../model/graph';
import { normalizeHealth, type GraphDoc, type MetricsDoc, type Resource } from '../model/types';
import { statusOf } from './subject';

export interface GuestRow {
  id: string;
  name: string;
  resource: Resource;
  /** Frame-style status; `progress` is separate. */
  status: TileStatus;
  /** A backup or migration is running (Proxmox's `lock`). */
  progress: boolean;
  word: string;
  cpu: number | null;
  mem: number | null;
  up: string;
  backup: string;
  host: string;
}

const LOCK_WORDS: Record<string, string> = {
  backup: 'backing up',
  migrate: 'migrating',
  snapshot: 'taking a snapshot',
  'snapshot-delete': 'deleting a snapshot',
  rollback: 'rolling back',
  clone: 'cloning',
  create: 'being created',
  suspending: 'suspending',
};

const WEEK = 7 * 86_400_000;

export function guestRow(g: GraphDoc | undefined, m: MetricsDoc | undefined, r: Resource, now: number): GuestRow {
  const h = normalizeHealth(r);
  const running = r.state === 'running';
  const autostart = attr(r, 'onboot') === true || r.desired === 'running';
  let status = statusOf(r);
  let word = '';
  const lock = attr<string>(r, 'lock');
  const cpu = running ? value(m, r.id, 'cpu.pct') : null;
  const mem = running ? value(m, r.id, 'mem.pct') : null;
  const lastOk = attr<string>(r, 'backup.last_ok');
  const lastStatus = attr<string>(r, 'backup.last_status');
  const lastAt = attr<string>(r, 'backup.last_at');
  if (status === 'down' || status === 'attention') {
    word = !running && autostart ? `stopped ${since(h.since ?? attr<string>(r, 'finished_at'), now)}, autostart on`.replace(' ,', ',') : (h.reason ?? '');
  } else if (status === 'off') {
    word = 'stopped (expected)';
  } else if (status === 'maintenance') {
    word = h.reason ?? 'maintenance';
  } else if (running) {
    const memWarn = series(m, r.id, 'mem.pct')?.warn ?? 90;
    if (mem !== null && mem >= memWarn) {
      status = 'attention';
      word = `memory ${Math.round(mem)}%`;
    } else if (lastStatus === 'failed') {
      status = 'attention';
      word = `backup failed${lastAt ? ` ${ago(lastAt, now)}` : ''}`;
    } else if (lastOk && now - Date.parse(lastOk) > WEEK && attr(r, 'backup.covered') !== false) {
      status = 'attention';
      word = `no backup in ${Math.floor((now - Date.parse(lastOk)) / 86_400_000)} days`;
    }
  }
  const progress = Boolean(lock);
  if (progress) word = LOCK_WORDS[lock!] ?? lock!;
  const started = attr<string>(r, 'started_at');
  return {
    id: r.id,
    name: label(r),
    resource: r,
    status,
    progress,
    word,
    cpu,
    mem,
    up: running && started && now >= Date.parse(started) ? duration(now - Date.parse(started)) : '—',
    backup: lastOk ? ago(lastOk, now) : lastStatus ? lastStatus : '—',
    host: label(outgoing(g, r.id, 'runs_on')[0]),
  };
}

const RANK = (r: GuestRow) =>
  r.status === 'down' ? 0 : r.status === 'attention' ? 1 : r.progress ? 2 : r.status === 'maintenance' ? 3 : r.status === 'off' ? 5 : 4;

/** Problems first, then work in progress, running guests, and stopped ones last. */
export function sortGuests(rows: GuestRow[]): GuestRow[] {
  return [...rows].sort((a, b) => RANK(a) - RANK(b) || (b.mem ?? -1) - (a.mem ?? -1) || a.name.localeCompare(b.name));
}
