/**
 * A tiny history-mode router. The app has a handful of routes, all owned by
 * the signed-in shell, so vue-router (about 10 kB gzip) would cost more than
 * this file does in the initial bundle. The hub already serves index.html for
 * any path that isn't a file, so every route survives a reload.
 *
 *   /                                  the front page (the first board)
 *   /boards/{id}                       the front page on that board
 *   /settings                          → /settings/integrations
 *   /settings/integrations             instances and the catalog
 *   /settings/integrations/new         pick an integration to add
 *   /settings/integrations/new/{type}  the add form for one integration
 *   /settings/integrations/{id}        edit an instance (?field=… focuses a field)
 *   /settings/stacks                   owned compose stacks per node, detected projects, stack secrets
 *   /settings/stacks/new               a new stack (saved as a draft)
 *   /settings/stacks/{node}/{name}     one stack: editor, plan, deploy, history
 *   /settings/import                   import from Homepage
 *   /settings/backup                   export this hub, import a Home file
 *   /settings/users                    users and roles (owner, admin)
 *   /settings/audit                    the audit log (owner, admin)
 *   /settings/notifications            this device's notifications; channels, rules, the delivery log (owner, admin)
 *   /settings/tokens                   API tokens: your own (the owner: everyone's; admins: all but the owner's)
 *   /settings/security                 the security level, step-up, passkeys
 *
 * Settings is a layer over the front page, not a separate app, so the board,
 * the realtime socket and the Inbox stay put underneath it.
 */
import { computed, shallowRef } from 'vue';

export interface Route {
  path: string;
  query: Record<string, string>;
}

export type SettingsPane = 'integrations' | 'stacks' | 'import' | 'backup' | 'users' | 'audit' | 'notifications' | 'tokens' | 'security';

const PANES: readonly string[] = ['stacks', 'import', 'backup', 'users', 'audit', 'notifications', 'tokens', 'security'];

export type View =
  | { name: 'front'; board?: string }
  | { name: 'settings'; pane: SettingsPane; id?: string; add?: string };

function read(): Route {
  if (typeof location === 'undefined') return { path: '/', query: {} };
  const query: Record<string, string> = {};
  new URLSearchParams(location.search).forEach((v, k) => (query[k] = v));
  return { path: location.pathname || '/', query };
}

export const route = shallowRef<Route>(read());

/** What the route asks the shell to show. Unknown paths are the front page. */
export function resolve(r: Route): View {
  let parts: string[];
  try {
    parts = r.path.split('/').filter(Boolean).map(decodeURIComponent);
  } catch {
    return { name: 'front' };
  }
  if (parts[0] === 'boards' && parts[1]) return { name: 'front', board: parts[1] };
  if (parts[0] !== 'settings') return { name: 'front' };
  const pane = (PANES.includes(parts[1] ?? '') ? parts[1] : 'integrations') as SettingsPane;
  if (pane === 'stacks') {
    // A stack is `{node}/{name}`, so its id carries the slash.
    if (parts[2] === 'new' && !parts[3]) return { name: 'settings', pane, add: '' };
    return parts[2] && parts[3] ? { name: 'settings', pane, id: `${parts[2]}/${parts[3]}` } : { name: 'settings', pane };
  }
  if (pane !== 'integrations') return { name: 'settings', pane };
  if (parts[2] === 'new') return parts[3] ? { name: 'settings', pane, add: parts[3] } : { name: 'settings', pane, add: '' };
  return parts[2] ? { name: 'settings', pane, id: parts[2] } : { name: 'settings', pane };
}

export const view = computed(() => resolve(route.value));

/** Go to a same-origin path. Pushed entries are marked, so `back()` knows it stays in the app. */
export function navigate(to: string, opts: { replace?: boolean } = {}): void {
  if (typeof location === 'undefined') return;
  const url = new URL(to, location.href);
  if (url.origin !== location.origin) return;
  const next = url.pathname + url.search + url.hash;
  if (next !== location.pathname + location.search + location.hash) {
    const depth = ((history.state as { depth?: number } | null)?.depth ?? 0) + (opts.replace ? 0 : 1);
    history[opts.replace ? 'replaceState' : 'pushState']({ depth }, '', next);
  }
  route.value = read();
}

/** Step back inside the app, or go to `fallback` when this entry was the first one. */
export function back(fallback = '/'): void {
  const depth = (history.state as { depth?: number } | null)?.depth ?? 0;
  if (depth > 0) history.back();
  else navigate(fallback, { replace: true });
}

/** Path for a settings pane (or an instance in it). */
export function settingsPath(pane: SettingsPane = 'integrations', id?: string, query?: Record<string, string | undefined>): string {
  let p = `/settings/${pane}`;
  if (id) p += `/${encodeURIComponent(id)}`;
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(query ?? {})) if (v) q.set(k, v);
  const qs = q.toString();
  return qs ? `${p}?${qs}` : p;
}

/** A notification's link on the front page (`/?incident=<id>`, `/?inbox=1`): the Inbox to open, focused on the incident, or null. */
export function inboxLink(r: Route): { incident: string } | null {
  if (resolve(r).name !== 'front') return null;
  if (r.query['incident']) return { incident: r.query['incident'] };
  return r.query['inbox'] ? { incident: '' } : null;
}

/** Path for one stack, or the stacks list without one. The new-stack form is `/settings/stacks/new`. */
export function stackPath(node?: string, name?: string): string {
  if (!node || !name) return '/settings/stacks';
  return `/settings/stacks/${encodeURIComponent(node)}/${encodeURIComponent(name)}`;
}

let started = false;

export function startRouter(): void {
  if (started || typeof window === 'undefined') return;
  started = true;
  window.addEventListener('popstate', () => (route.value = read()));
  if (route.value.path === '/settings' || route.value.path === '/settings/') navigate('/settings/integrations', { replace: true });
}

/** For tests: re-read the location. */
export function syncRoute(): void {
  route.value = read();
}
