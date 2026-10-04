/**
 * What the command bar (⌘K) can do, built from what the page knows: boards,
 * resources in the Home Graph, integrations, the Inbox, edit mode, themes,
 * settings and signing out. Pure data plus `run` callbacks; searching is
 * lib/fuzzy.ts, ranking and grouping are here so they're testable without Vue.
 */
import { rank, type Searchable } from '../lib/fuzzy';
import { THEMES, type ThemeId } from '../lib/theme';
import { label, placeOf, resources } from './graph';
import type { GraphDoc, InboxDoc } from './types';
import type { Instance } from './integrations';

export type CommandGroup = 'Actions' | 'Inbox' | 'Boards' | 'Resources' | 'Integrations' | 'Settings' | 'Theme' | 'Account';

export interface Command extends Searchable {
  id: string;
  group: CommandGroup;
  /** Right-hand hint: a kind, a place, a tier or a key. */
  hint?: string;
  /** Shown as a key cap. */
  kbd?: string;
  run: () => void;
}

/** Groups in the order they show when nothing is typed. */
export const GROUP_ORDER: readonly CommandGroup[] = ['Actions', 'Inbox', 'Boards', 'Integrations', 'Settings', 'Theme', 'Account', 'Resources'];

const KIND_WORDS: Record<string, string> = {
  app: 'service',
  container: 'container',
  vm: 'VM',
  lxc: 'LXC',
  host: 'host',
  engine: 'Docker engine',
  integration: 'integration',
  camera: 'camera',
  router: 'router',
  route: 'route',
  backup_target: 'backups',
  backup_job: 'backup job',
  array: 'array',
  pool: 'pool',
  disk: 'disk',
  share: 'share',
  storage: 'storage',
  cluster: 'cluster',
  wan: 'WAN',
};

export interface CommandContext {
  graph?: GraphDoc | undefined;
  inbox?: InboxDoc | undefined;
  boards?: readonly { id: string; name: string }[];
  currentBoard?: string;
  instances?: readonly Instance[];
  editing?: boolean;
  theme?: ThemeId;
  username?: string;
  do: {
    openInbox: (incident?: string) => void;
    toggleEdit: () => void;
    openBoard: (id: string) => void;
    reveal: (resourceId: string) => void;
    openSettings: (pane: 'integrations' | 'import' | 'security', id?: string) => void;
    addIntegration: () => void;
    setTheme: (id: ThemeId) => void;
    signOut: () => void;
  };
}

type CommandInput = Omit<Command, 'hint'> & { hint?: string | undefined };

/** Drop an undefined hint (exact optional properties). */
function cmd(c: CommandInput): Command {
  const { hint, ...rest } = c;
  return hint === undefined ? rest : { ...rest, hint };
}

export function buildCommands(ctx: CommandContext): Command[] {
  const d = ctx.do;
  const out: CommandInput[] = [
    { id: 'inbox', group: 'Actions', label: 'Open Inbox', keywords: 'repairs problems incidents', kbd: 'I', run: () => d.openInbox() },
    {
      id: 'edit',
      group: 'Actions',
      label: ctx.editing ? 'Done editing layout' : 'Edit layout',
      keywords: 'edit mode arrange move resize tiles',
      kbd: 'E',
      run: d.toggleEdit,
    },
  ];

  for (const i of Object.values(ctx.inbox?.incidents ?? {})) {
    if (i.status !== 'open') continue;
    out.push({
      id: `incident:${i.id}`,
      group: 'Inbox',
      label: i.title,
      keywords: `${i.class} ${i.target} ${i.summary}`,
      hint: i.class === 'repair' ? 'repair' : i.severity === 'down' ? 'down' : 'needs you',
      run: () => d.openInbox(i.id),
    });
  }

  for (const b of ctx.boards ?? []) {
    out.push({ id: `board:${b.id}`, group: 'Boards', label: b.name, keywords: `board ${b.id}`, hint: b.id === ctx.currentBoard ? 'open now' : 'board', run: () => d.openBoard(b.id) });
  }

  for (const x of ctx.instances ?? []) {
    out.push({
      id: `integration:${x.id}`,
      group: 'Integrations',
      label: x.name,
      keywords: `integration ${x.integration} ${x.id} ${x.status}`,
      hint: x.integration,
      run: () => d.openSettings('integrations', x.id),
    });
  }
  out.push({ id: 'integration:add', group: 'Integrations', label: 'Add an integration', keywords: 'connect new integration', run: d.addIntegration });

  out.push(
    { id: 'settings:integrations', group: 'Settings', label: 'Settings: Integrations', keywords: 'connections tokens keys', run: () => d.openSettings('integrations') },
    { id: 'settings:import', group: 'Settings', label: 'Import from Homepage', keywords: 'settings services.yaml docker.yaml migrate', run: () => d.openSettings('import') },
    { id: 'settings:security', group: 'Settings', label: 'Settings: Security', keywords: 'passkeys webauthn touch id face id', run: () => d.openSettings('security') },
  );

  for (const t of THEMES) {
    out.push({ id: `theme:${t.id}`, group: 'Theme', label: `Theme: ${t.name}`, keywords: `appearance ${t.note}`, hint: t.id === ctx.theme ? 'current' : undefined, run: () => d.setTheme(t.id) });
  }

  out.push({ id: 'signout', group: 'Account', label: 'Sign out', keywords: `log out ${ctx.username ?? ''}`, hint: ctx.username, run: d.signOut });

  const g = ctx.graph;
  for (const r of resources(g)) {
    const kind = KIND_WORDS[r.kind] ?? r.kind;
    const place = placeOf(g, r.id);
    out.push({
      id: `resource:${r.id}`,
      group: 'Resources',
      label: label(r),
      keywords: `${r.id} ${r.kind} ${kind} ${r.source ?? ''} ${place ?? ''}`,
      hint: place ? `${kind} · ${place}` : kind,
      run: () => d.reveal(r.id),
    });
  }
  return out.map(cmd);
}

export interface CommandSection {
  group: CommandGroup;
  items: Command[];
}

/**
 * Search and group. Empty query: everything but resources, in group order.
 * With a query: matches only, groups ordered by their best match, at most
 * `perGroup` items each (resources get the most room).
 */
export function searchCommands(all: readonly Command[], query: string, perGroup = 6): CommandSection[] {
  const q = query.trim();
  const list = q ? rank(q, all) : all.filter((c) => c.group !== 'Resources');
  const sections = new Map<CommandGroup, Command[]>();
  for (const c of list) {
    const items = sections.get(c.group) ?? [];
    const cap = !q ? Infinity : c.group === 'Resources' ? perGroup + 4 : perGroup;
    if (items.length < cap) items.push(c);
    sections.set(c.group, items);
  }
  const out = [...sections.entries()].map(([group, items]) => ({ group, items }));
  if (!q) out.sort((a, b) => GROUP_ORDER.indexOf(a.group) - GROUP_ORDER.indexOf(b.group));
  return out;
}
