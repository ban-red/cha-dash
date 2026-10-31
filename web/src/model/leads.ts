/**
 * Leads: services the hub already sees through what's connected, offered as
 * connections to make (docs/plan/11-import-and-discovery.md → Discover).
 * Pure functions of the graph and the instances; finding them touches no
 * network. Checking one (POST /api/discover {urls}) probes only its URL.
 *
 * A lead comes from:
 * - a container whose `homepage.widget.type` label or image is a known
 *   integration (catalogMatch.ts), at its `homepage.href` label, else its
 *   first published port on the Docker host it runs on;
 * - a reverse-proxy route whose host names a known integration
 *   (`sonarr.home.example`), or that goes to such a container, at the
 *   route's URL.
 *
 * One service found several ways is one lead listing every way; its URL
 * prefers the route over the label over the published port. Finds are one
 * service when they share a container (the correlator's `same_as`, and a
 * route's `routes_to`) or an address (scheme and path aside), or when an
 * integration has only one address among them.
 *
 * A lead an instance already reads isn't offered: an app of that
 * integration runs on its container (the correlator's `runs_on`), an
 * instance has its address (scheme aside), or it's the only one of that
 * integration and has no address. Nor is a proxy's route to itself (its
 * dashboard and API, which go to no container), nor a dismissed lead.
 */
import { KNOWN_INTEGRATIONS, knownIntegrationOf, type KnownIntegration } from './catalogMatch';
import { attr } from './graph';
import type { GraphDoc, Resource } from './types';

export type Way = 'container' | 'label' | 'route';

export interface Lead {
  /** Stable: the integration and the address (or the first resource), for dismissals. */
  key: string;
  integration: string;
  name: string;
  url?: string;
  /** The link a tile opens: the route's or the label's, when there is one. */
  href?: string;
  ways: { way: Way; text: string; resource: string }[];
}

interface InstanceLike {
  id: string;
  integration: string;
  config?: Record<string, unknown>;
}

/** scheme://host[:port][/path] in lower case, without a default port or trailing slash. */
export function origin(u: string): string {
  try {
    const x = new URL(u);
    if (x.protocol !== 'http:' && x.protocol !== 'https:') return '';
    const path = x.pathname.replace(/\/+$/, '');
    return `${x.protocol}//${x.host.toLowerCase()}${path}`;
  } catch {
    return '';
  }
}

/** An address without its scheme: http and https on one host are one service. */
const bare = (u: string): string => origin(u).replace(/^https?:\/\//, '');

const RANK: Record<Way, number> = { route: 0, label: 1, container: 2 };

/** The Docker host a container's instance reaches over tcp://, if any. */
function dockerHost(containerId: string, instances: readonly InstanceLike[]): string {
  const m = /^docker:([^/]+)\//.exec(containerId);
  const inst = m && instances.find((i) => i.id === m[1]);
  const ep = typeof inst?.config?.['endpoint'] === 'string' ? (inst.config['endpoint'] as string) : '';
  const t = /^tcp:\/\/([^:/]+)/.exec(ep);
  return t?.[1] ?? '';
}

/** The first published host port: "8989->8989/tcp" or "0.0.0.0:8989->…". */
function publishedPort(r: Resource): string {
  const pub = attr<unknown>(r, 'published');
  const first = Array.isArray(pub) && typeof pub[0] === 'string' ? (pub[0] as string) : '';
  const host = first.split('->')[0] ?? '';
  return /(\d+)$/.exec(host)?.[1] ?? '';
}

function byRouteHost(host: string): KnownIntegration | undefined {
  const label = host.split('.')[0]?.toLowerCase() ?? '';
  return KNOWN_INTEGRATIONS.find((k) => k.id === label || k.images.includes(label) || k.homepage?.includes(label));
}

/** An integration's name, from the catalog matcher's table, without the catalog loaded. */
export const integrationName = (id: string): string => KNOWN_INTEGRATIONS.find((k) => k.id === id)?.name ?? id;

export function leadsOf(g: GraphDoc | undefined, instances: readonly InstanceLike[], dismissed: ReadonlySet<string> = new Set()): Lead[] {
  if (!g) return [];
  type Found = { known: KnownIntegration; way: Way; text: string; resource: string; url: string; name: string; container: string; read: boolean };
  // The correlator's view, indexed once: Docker's container for Unraid's,
  // the apps on each container, and where each route goes.
  const sameAs = new Map<string, string>();
  const appsOn = new Map<string, string[]>();
  const routesTo = new Map<string, Resource[]>();
  for (const rel of Object.values(g.relations)) {
    if (rel.type === 'same_as') sameAs.set(rel.from, rel.to);
    else if (rel.type === 'runs_on' && g.resources[rel.from]?.kind === 'app') appsOn.set(rel.to, [...(appsOn.get(rel.to) ?? []), rel.from]);
    else if (rel.type === 'routes_to' && g.resources[rel.to]) routesTo.set(rel.from, [...(routesTo.get(rel.from) ?? []), g.resources[rel.to]!]);
  }
  const canonical = (id: string) => sameAs.get(id) ?? id;
  const readOn = (container: string, integration: string) => (appsOn.get(container) ?? []).some((a) => a.startsWith(`${integration}:`));
  const integrationOfSource = (id: string) => instances.find((i) => i.id === /^[a-z0-9_-]+:([^/]+)\//.exec(id)?.[1])?.integration;
  const found: Found[] = [];
  for (const r of Object.values(g.resources)) {
    if (r.kind === 'container') {
      const known = knownIntegrationOf(r);
      if (!known) continue;
      const href = origin(r.labels?.['homepage.href'] ?? '');
      const host = dockerHost(r.id, instances);
      const port = publishedPort(r);
      const byLabel = Boolean(r.labels?.['homepage.widget.type']);
      const container = canonical(r.id);
      found.push({
        known,
        way: href || byLabel ? 'label' : 'container',
        text: href ? `label homepage.href on ${r.name ?? r.id}` : `container ${r.name ?? r.id}${port ? ` · :${port}` : ''}`,
        resource: r.id,
        url: href || (host && port ? `http://${host}:${port}` : ''),
        name: r.labels?.['homepage.name'] || known.name,
        container,
        read: readOn(container, known.id),
      });
    } else if (r.kind === 'route') {
      const u = origin(attr<string>(r, 'url') ?? '');
      if (!u) continue;
      const targets = routesTo.get(r.id) ?? [];
      const box = targets.find((t) => t.kind === 'container');
      // The host names the service (sonarr.home.example), or the route goes to its container.
      const known = byRouteHost(new URL(u).hostname) ?? (box && knownIntegrationOf(box));
      if (!known) continue;
      const container = box ? canonical(box.id) : '';
      // A proxy's own dashboard: published by an instance of that very
      // integration, going to no container. Or a route to a connected app.
      const itself = !container && integrationOfSource(r.id) === known.id;
      const toApp = targets.some((t) => t.kind === 'app' && t.id.startsWith(`${known.id}:`));
      found.push({ known, way: 'route', text: `routed as ${new URL(u).host}`, resource: r.id, url: u, name: known.name, container, read: itself || toApp || (container !== '' && readOn(container, known.id)) });
    }
  }

  // One lead per service, within each integration: finds that share a
  // container or a bare host are one; so are all of an integration's finds
  // when they have at most one address.
  const leads = new Map<string, Lead & { rank: number; read: boolean }>();
  const byIntegration = new Map<string, Found[]>();
  for (const f of found) byIntegration.set(f.known.id, [...(byIntegration.get(f.known.id) ?? []), f]);
  for (const [id, fs] of byIntegration) {
    const parent = fs.map((_, i) => i);
    const root = (i: number): number => (parent[i] === i ? i : (parent[i] = root(parent[i]!)));
    const owner = new Map<string, number>();
    const hostOf = (u: string) => bare(u).split('/')[0] ?? '';
    fs.forEach((f, i) => {
      for (const k of [f.container && `c:${f.container}`, f.url && `h:${hostOf(f.url)}`]) {
        if (!k) continue;
        const j = owner.get(k);
        if (j === undefined) owner.set(k, i);
        else parent[root(i)] = root(j);
      }
    });
    if (new Set(fs.map((f) => f.url && bare(f.url)).filter(Boolean)).size <= 1) fs.forEach((_, i) => (parent[root(i)] = root(0)));
    const groups = new Map<number, Found[]>();
    fs.forEach((f, i) => groups.set(root(i), [...(groups.get(root(i)) ?? []), f]));
    for (const grp of groups.values()) {
      const byWay = [...grp].sort((a, b) => RANK[a.way] - RANK[b.way] || Number(!a.url) - Number(!b.url) || a.url.length - b.url.length);
      const best = byWay[0]!;
      const url = best.url || grp.find((f) => f.url)?.url || '';
      const key = `${id}|${url || best.resource}`;
      const lead: Lead & { rank: number; read: boolean } = {
        key,
        integration: id,
        name: grp.find((f) => f.way === 'label')?.name ?? best.name,
        ways: byWay.map((f) => ({ way: f.way, text: f.text, resource: f.resource })),
        rank: RANK[best.way],
        read: grp.some((f) => f.read),
      };
      if (url) lead.url = url;
      const href = grp.find((f) => f.way === 'route')?.url ?? grp.find((f) => f.way === 'label')?.url;
      if (href) lead.href = href;
      // Whichever find wins, every address it was seen at counts as read.
      const urls = grp.map((f) => f.url).filter(Boolean);
      if (instances.some((i) => i.integration === id && typeof i.config?.['url'] === 'string' && urls.some((u) => bare(u) === bare(i.config!['url'] as string)))) lead.read = true;
      leads.set(key, lead);
    }
  }

  const onlyOne = (l: Lead) => !l.url && instances.some((i) => i.integration === l.integration);
  return [...leads.values()]
    .filter((l) => !dismissed.has(l.key) && !l.read && !onlyOne(l))
    .sort((a, b) => a.rank - b.rank || a.name.localeCompare(b.name))
    .map(({ rank: _rank, read: _read, ...l }) => l);
}

/** What the discovery import source takes: one entry per lead. */
export function leadsJSON(leads: readonly Lead[]): string {
  return JSON.stringify(
    leads.map((l) => {
      const o: Record<string, string> = { integration: l.integration, name: l.name };
      if (l.url) o['url'] = l.url;
      if (l.href && l.href !== l.url) o['href'] = l.href;
      return o;
    }),
  );
}
