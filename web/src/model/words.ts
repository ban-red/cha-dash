/** Plain words for graph vocabulary in the Inbox and confirm dialogs. */
import { label } from './graph';
import { isHub } from './hub';
import type { GraphDoc } from './types';

const RELATIONS: Record<string, string> = {
  runs_on: 'runs on it',
  depends_on: 'depends on it',
  stores_on: 'stores data on it',
  backed_up_by: 'loses its backups',
  routes_to: 'route breaks',
  monitors: 'goes stale',
  part_of: 'part of it',
  provides_network_for: 'loses network',
  exposes: 'feed stops',
};

export const relationWords = (via: string): string => RELATIONS[via] ?? via.replace(/_/g, ' ');

/** "homeassistant (VM)" style names for impact lists; the hub is "Cha Dash itself". */
export function impactName(g: GraphDoc | undefined, id: string): string {
  if (isHub(g, id)) return 'Cha Dash itself';
  const r = g?.resources[id];
  if (!r) return id;
  const kind: Record<string, string> = { vm: 'VM', lxc: 'LXC', container: 'container', route: 'route', backup_job: 'backup job', app: '' };
  const k = kind[r.kind] ?? r.kind;
  const name = r.kind === 'backup_job' ? `${id.split('/')[0]?.replace(/^pve:/, '') ?? ''} nightly` : label(r);
  return k ? `${name} · ${k}` : name;
}

/** A fact value for display: ISO times as times, numbers grouped, the rest as text. */
export function factValue(v: unknown, fmtTime: (iso: string) => string): string {
  if (typeof v === 'number') return v.toLocaleString('en-US');
  if (typeof v === 'string' && /^\d{4}-\d{2}-\d{2}T/.test(v)) return fmtTime(v);
  if (typeof v === 'boolean') return v ? 'yes' : 'no';
  if (v === null || v === undefined) return '—';
  return String(v);
}

/** "used_pct" → "Used %", "full_in_days" → "Full in days". */
export function factLabel(key: string): string {
  const s = key
    .replace(/_pct$/, ' %')
    .replace(/_gb$/, ' (GB)')
    .replace(/_tb$/, ' (TB)')
    .replace(/_mib$/, ' (MiB)')
    .replace(/_/g, ' ');
  return s[0]!.toUpperCase() + s.slice(1);
}

/** Does an impact list take Cha Dash itself down? (Its resources, by id.) */
export function hitsHub(g: GraphDoc | undefined, ids: readonly string[]): boolean {
  return ids.some((id) => isHub(g, id));
}

/** The sentence a confirmation shows when the hub is in the blast radius. */
export const HUB_WARNING = 'This takes Cha Dash itself down: this page goes offline until it’s back. The task still runs to the end and is audited.';

/** An impact list with Cha Dash itself first (the rest in their order). */
export function hubFirst<T extends { resource: string }>(g: GraphDoc | undefined, xs: readonly T[]): T[] {
  return [...xs.filter((x) => isHub(g, x.resource)), ...xs.filter((x) => !isHub(g, x.resource))];
}
