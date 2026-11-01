/**
 * Backup coverage (the `backups` topic, internal/backups): grouping the
 * map by state, the words for an objective, and the actions a row offers.
 * Pure: the Settings → Backups pane and the coverage widget read it.
 *
 * Run now is Proxmox's `proxmox.guest.backup` on the guest (for a
 * container that inherits its guest's backup, on that guest). Verify now
 * is `pbs.datastore.verify` on the PBS datastore a guest's job writes to,
 * narrowed to the guest's snapshot group (`vm/<vmid>`, `ct/<vmid>`).
 * Both are T1; the hub decides whether they're available.
 */
import { ago, duration } from '../lib/format';
import type { GlyphKind } from '../ui/marks/Glyph.vue';
import { attr, resolve } from './graph';
import type { BackupItem, BackupState, BackupsDoc, GraphDoc } from './types';

/** The states in the order a list shows them: what needs you first. */
export const BACKUP_STATES: readonly BackupState[] = ['stale', 'uncovered', 'unknown', 'fresh'];

export const STATE_WORDS: Record<BackupState, { title: string; short: string }> = {
  stale: { title: 'Covered but stale', short: 'stale' },
  uncovered: { title: 'Not covered', short: 'not covered' },
  unknown: { title: 'Unknown', short: 'unknown' },
  fresh: { title: 'Covered and fresh', short: 'fresh' },
};

export const STATE_GLYPH: Record<BackupState, GlyphKind> = { stale: 'attn', uncovered: 'cross', unknown: 'off', fresh: 'check' };

/** Words for a kind on the map. */
export function kindWord(kind: string): string {
  switch (kind) {
    case 'vm':
      return 'VM';
    case 'lxc':
      return 'LXC';
    case 'container':
      return 'container';
    case 'share':
      return 'share';
    case 'disk':
      return 'flash drive';
  }
  return kind;
}

export function items(doc: BackupsDoc | undefined): BackupItem[] {
  return Object.values(doc?.items ?? {}).sort((a, b) => a.name.localeCompare(b.name) || a.resource.localeCompare(b.resource));
}

export interface BackupGroup {
  state: BackupState;
  title: string;
  items: BackupItem[];
}

/** The map grouped by state, stale first; empty groups left out. */
export function groups(doc: BackupsDoc | undefined): BackupGroup[] {
  const all = items(doc);
  return BACKUP_STATES.map((state) => ({ state, title: STATE_WORDS[state].title, items: all.filter((i) => i.state === state) })).filter((g) => g.items.length);
}

export function counts(doc: BackupsDoc | undefined): Record<BackupState, number> {
  const out: Record<BackupState, number> = { stale: 0, uncovered: 0, unknown: 0, fresh: 0 };
  for (const i of items(doc)) out[i.state]++;
  return out;
}

/** An objective in words: "26 hours", "8 days". */
export function objectiveWords(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '—';
  const h = Math.round(ms / 3_600_000);
  if (h < 48) return h === 1 ? '1 hour' : `${h} hours`;
  const d = Math.round(ms / 86_400_000);
  return `${d} days`;
}

/** Where the objective comes from, for the row. */
export function objectiveSource(i: BackupItem): string {
  if (i.objective === 'override') return 'set here';
  if (i.objective === 'schedule') return 'from the schedule';
  return 'the default';
}

/** "backed up 3h ago" or "never backed up". */
export function lastWords(i: BackupItem, now: number): string {
  if (!i.last_ok) return i.state === 'uncovered' ? 'never backed up' : 'no good backup seen';
  return `backed up ${ago(i.last_ok, now)}`;
}

/** How far over its objective a stale item is: "4h over". */
export function overBy(i: BackupItem, now: number): string {
  if (i.state !== 'stale' || !i.last_ok) return '';
  const over = now - Date.parse(i.last_ok) - i.max_age_ms;
  return over > 0 ? `${duration(over)} over` : '';
}

/** What backs it up, in words: "daily (pbs-main → main) · through pvedocker's backup". */
export function coverWords(i: BackupItem): string {
  const live = i.by.filter((c) => !c.disabled);
  const by = live.map((c) => (c.target_name ? `${c.name} → ${c.target_name}` : c.name)).join(', ');
  const parts = [by, i.through_name ? `through ${i.through_name}’s backup` : ''].filter(Boolean);
  return parts.join(' · ');
}

export interface BackupAction {
  action: string;
  target: string;
  label: string;
  params?: Record<string, unknown>;
}

/** The guest whose backup runs for this item: itself, or the guest a container inherits from. */
function guestOf(i: BackupItem): string | undefined {
  if (i.kind === 'vm' || i.kind === 'lxc') return i.resource;
  return i.through;
}

/** Run now: PVE's vzdump of the guest (T1). Only for Proxmox guests. */
export function runNow(i: BackupItem, g: GraphDoc | undefined): BackupAction | undefined {
  const guest = guestOf(i);
  if (!guest) return undefined;
  const r = resolve(g, guest);
  if (!guest.startsWith('proxmox:') && !r?.source?.includes('proxmox')) return undefined;
  const name = i.through_name ?? i.name;
  return { action: 'proxmox.guest.backup', target: guest, label: `Back up ${name} now` };
}

/** Verify now: PBS verifies the guest's snapshot group on the datastore its job writes to (T1). */
export function verifyNow(i: BackupItem, g: GraphDoc | undefined): BackupAction | undefined {
  const cover = i.by.find((c) => !c.disabled && c.target?.startsWith('pbs:'));
  if (!cover?.target) return undefined;
  const guest = guestOf(i);
  const r = guest ? resolve(g, guest) : undefined;
  const vmid = attr<number | string>(r, 'vmid');
  const type = r?.kind === 'lxc' ? 'ct' : r?.kind === 'vm' ? 'vm' : undefined;
  const params = type && vmid !== undefined ? { group: `${type}/${vmid}` } : undefined;
  const store = cover.target_name ?? cover.target;
  return {
    action: 'pbs.datastore.verify',
    target: cover.target,
    label: params ? `Verify ${i.through_name ?? i.name}’s backups on ${store}` : `Verify ${store} now`,
    ...(params ? { params } : {}),
  };
}

/** The objective choices the pane offers, in hours. */
export const OBJECTIVE_CHOICES: readonly { hours: number; label: string }[] = [
  { hours: 6, label: '6 hours' },
  { hours: 12, label: '12 hours' },
  { hours: 26, label: '26 hours (daily)' },
  { hours: 50, label: '2 days' },
  { hours: 74, label: '3 days' },
  { hours: 8 * 24, label: '8 days (weekly)' },
  { hours: 15 * 24, label: '15 days' },
  { hours: 32 * 24, label: '32 days (monthly)' },
];
