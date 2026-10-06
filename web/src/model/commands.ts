/**
 * What the command bar (⌘K) can do, built from what the page knows: boards,
 * resources in the Home Graph, integrations, the Inbox, edit mode, themes,
 * settings and signing out, plus actions on resources (`actionCommands`):
 * every open incident's suggested action and alternatives, and what the hub
 * lists for the resource in focus (`GET /api/actions?target=`). Pure data
 * plus `run` callbacks; searching is lib/fuzzy.ts, ranking and grouping are
 * here so they're testable without Vue.
 */
import { rank, type Searchable } from '../lib/fuzzy';
import { THEMES, type ThemeId } from '../lib/theme';
import { distinct, label, placeOf } from './graph';
import type { Action, GraphDoc, InboxDoc, Incident, Tier } from './types';
import type { Instance } from './integrations';
import type { SettingsPane } from '../lib/router';
import type { Permissions } from './roles';

export type CommandGroup = 'Run' | 'Actions' | 'Inbox' | 'Boards' | 'Resources' | 'Integrations' | 'Settings' | 'Theme' | 'Account';

export interface Command extends Searchable {
  id: string;
  group: CommandGroup;
  /** Right-hand hint: a kind, a place, a tier or a key. */
  hint?: string;
  /** Shown as a key cap. */
  kbd?: string;
  /** Can't run here: the sentence why (the option is shown, marked unavailable). */
  disabled?: string;
  run: () => void;
}

/** Groups in the order they show when nothing is typed. */
export const GROUP_ORDER: readonly CommandGroup[] = ['Run', 'Actions', 'Inbox', 'Boards', 'Integrations', 'Settings', 'Theme', 'Account', 'Resources'];

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
  media_request: 'request',
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
  /** What the viewer's role may use (model/roles.ts); everything when absent. */
  can?: Pick<Permissions, 'inbox' | 'settings' | 'integrations' | 'editBoards' | 'panes'>;
  do: {
    openInbox: (incident?: string) => void;
    toggleEdit: () => void;
    /** Edit mode with the widget picker open on the current board. */
    addWidget?: () => void;
    openBoard: (id: string) => void;
    reveal: (resourceId: string) => void;
    openSettings: (pane: SettingsPane, id?: string) => void;
    addIntegration: () => void;
    setTheme: (id: ThemeId) => void;
    signOut: () => void;
  };
}

type CommandInput = Omit<Command, 'hint' | 'disabled'> & { hint?: string | undefined; disabled?: string | undefined };

/** Drop an undefined hint or reason (exact optional properties). */
function cmd(c: CommandInput): Command {
  const { hint, disabled, ...rest } = c;
  const out: Command = rest;
  if (hint !== undefined) out.hint = hint;
  if (disabled !== undefined) out.disabled = disabled;
  return out;
}

/** One action the hub lists for a resource (lib/actions.ts `Offer`). */
export interface ActionOffer {
  id: string;
  label: string;
  tier: Tier;
  available: boolean;
  unavailable?: string;
  household?: boolean;
}

export interface ActionContext {
  graph?: GraphDoc | undefined;
  inbox?: InboxDoc | undefined;
  /** The resource in focus (the focused tile, or the top search result), whose offers these are. */
  subject?: string | undefined;
  offers?: readonly ActionOffer[];
  /** Only the subject's actions (nothing typed yet), or every open incident's too. */
  onlySubject?: boolean;
  /** Whether the viewer's role can run a tier (model/roles.ts); everything when absent. */
  can?: Pick<Permissions, 'act'>;
  /** Run an incident's action (its progress shows at the incident). */
  act: (incident: Incident, action: Action) => void;
  /** Run an action the hub listed, through the same ladder. */
  run: (offer: ActionOffer, target: string) => void;
  /** Say why an unavailable action can't run. */
  refuse: (why: string) => void;
}

/**
 * Actions as commands, in the Run group: every open incident's suggested
 * action and alternatives the role may run (not links), then the hub's
 * offers for the subject that no incident already covers. An offer the hub
 * says is unavailable stays listed, disabled, with its sentence. Each runs
 * the normal safety ladder: the hub plans it, and the plan's tier decides
 * how it's confirmed.
 */
export function actionCommands(ctx: ActionContext): Command[] {
  const out: CommandInput[] = [];
  const seen = new Set<string>();
  const nameOf = (id: string) => label(ctx.graph?.resources[id]) || id;
  const allowed = (tier: Tier | string, household?: boolean) => !ctx.can || ctx.can.act(tier, household);
  for (const i of Object.values(ctx.inbox?.incidents ?? {})) {
    if (i.status !== 'open' || i.class === 'suggestion') continue;
    for (const a of [i.suggested, ...(i.alternatives ?? [])]) {
      if (!a || a.id === 'link.open' || !allowed(a.tier, a.household)) continue;
      if (ctx.onlySubject && a.target !== ctx.subject) continue;
      const key = `${a.id}@${a.target}`;
      if (seen.has(key)) continue;
      seen.add(key);
      const why = a.unavailable ?? (a.plan_ready === false ? 'Cha Dash can’t run this here yet.' : undefined);
      out.push({
        id: `act:${key}`,
        group: 'Run',
        label: a.label,
        keywords: `${a.id} ${a.target} ${nameOf(a.target)} ${i.title}`,
        hint: why ? 'unavailable' : a.tier,
        disabled: why,
        run: () => (why ? ctx.refuse(why) : ctx.act(i, a)),
      });
    }
  }
  const subject = ctx.subject;
  if (subject) {
    for (const o of ctx.offers ?? []) {
      const key = `${o.id}@${subject}`;
      if (seen.has(key) || !allowed(o.tier, o.household)) continue;
      seen.add(key);
      const why = o.available ? undefined : (o.unavailable ?? 'Cha Dash can’t run this here yet.');
      out.push({
        id: `act:${key}`,
        group: 'Run',
        label: o.label,
        keywords: `${o.id} ${subject} ${nameOf(subject)}`,
        hint: why ? 'unavailable' : o.tier,
        disabled: why,
        run: () => (why ? ctx.refuse(why) : ctx.run(o, subject)),
      });
    }
  }
  return out.map(cmd);
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
  if (d.addWidget && ctx.currentBoard) {
    out.push({ id: 'add-widget', group: 'Actions', label: 'Add widget', keywords: 'add new widget tile card catalog picker edit layout', run: d.addWidget });
  }

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
    { id: 'settings:stacks', group: 'Settings', label: 'Settings: Stacks', keywords: 'compose docker deploy rollback adopt yaml', run: () => d.openSettings('stacks') },
    { id: 'settings:import', group: 'Settings', label: 'Import from Homepage', keywords: 'settings services.yaml docker.yaml migrate', run: () => d.openSettings('import') },
    { id: 'settings:backup', group: 'Settings', label: 'Settings: Backup', keywords: 'export import restore yaml home file secrets', run: () => d.openSettings('backup') },
    { id: 'settings:users', group: 'Settings', label: 'Settings: Users', keywords: 'people roles accounts household owner admin', run: () => d.openSettings('users') },
    { id: 'settings:audit', group: 'Settings', label: 'Settings: Audit', keywords: 'log history who did what actions', run: () => d.openSettings('audit') },
    { id: 'settings:notifications', group: 'Settings', label: 'Settings: Notifications', keywords: 'push ntfy webhook alerts phone devices', run: () => d.openSettings('notifications') },
    { id: 'settings:tokens', group: 'Settings', label: 'Settings: API tokens', keywords: 'api bearer script automation curl scopes', run: () => d.openSettings('tokens') },
    { id: 'settings:security', group: 'Settings', label: 'Settings: Security', keywords: 'passkeys webauthn touch id face id step-up', run: () => d.openSettings('security') },
  );

  for (const t of THEMES) {
    out.push({ id: `theme:${t.id}`, group: 'Theme', label: `Theme: ${t.name}`, keywords: `appearance ${t.note}`, hint: t.id === ctx.theme ? 'current' : undefined, run: () => d.setTheme(t.id) });
  }

  out.push({ id: 'signout', group: 'Account', label: 'Sign out', keywords: `log out ${ctx.username ?? ''}`, hint: ctx.username, run: d.signOut });

  const g = ctx.graph;
  for (const r of distinct(g)) {
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
  return out.filter((c) => allowed(c, ctx.can)).map(cmd);
}

/** Hide what the viewer's role can't use: the Inbox, Settings and its panes, edit mode. */
function allowed(c: CommandInput, can: CommandContext['can']): boolean {
  if (!can) return true;
  if (c.group === 'Inbox' || c.id === 'inbox') return can.inbox;
  if (c.id === 'edit' || c.id === 'add-widget') return can.editBoards;
  if (c.group === 'Integrations') return can.integrations;
  if (c.group === 'Settings') {
    const pane = c.id === 'settings:import' ? 'import' : c.id.replace(/^settings:/, '');
    return can.settings && (can.panes as readonly string[]).includes(pane);
  }
  return true;
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
