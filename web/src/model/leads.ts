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
 *   (`sonarr.home.example`), at the route's URL.
 *
 * One service found several ways is one lead listing every way; its URL
 * prefers the route over the label over the published port. A lead an
 * instance already reads (same integration and address, or the only one of
 * that integration when the lead has no address) isn't offered, nor is one
 * that was dismissed.
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
  type Found = { known: KnownIntegration; way: Way; text: string; resource: string; url: string; name: string };
  const found: Found[] = [];
  for (const r of Object.values(g.resources)) {
    if (r.kind === 'container') {
      const known = knownIntegrationOf(r);
      if (!known) continue;
      const href = origin(r.labels?.['homepage.href'] ?? '');
      const host = dockerHost(r.id, instances);
      const port = publishedPort(r);
      const byLabel = Boolean(r.labels?.['homepage.widget.type']);
      found.push({
        known,
        way: href || byLabel ? 'label' : 'container',
        text: href ? `label homepage.href on ${r.name ?? r.id}` : `container ${r.name ?? r.id}${port ? ` · :${port}` : ''}`,
        resource: r.id,
        url: href || (host && port ? `http://${host}:${port}` : ''),
        name: r.labels?.['homepage.name'] || known.name,
      });
    } else if (r.kind === 'route') {
      const u = origin(attr<string>(r, 'url') ?? '');
      if (!u) continue;
      const known = byRouteHost(new URL(u).hostname);
      if (!known) continue;
      found.push({ known, way: 'route', text: `routed as ${new URL(u).host}`, resource: r.id, url: u, name: known.name });
    }
  }

  // One lead per service: the same address, or the only finds of an
  // integration that has a single address among them.
  const leads = new Map<string, Lead & { rank: number }>();
  const byIntegration = new Map<string, Found[]>();
  for (const f of found) byIntegration.set(f.known.id, [...(byIntegration.get(f.known.id) ?? []), f]);
  for (const [id, fs] of byIntegration) {
    const urls = new Set(fs.map((f) => f.url).filter(Boolean));
    const groups = urls.size <= 1 ? [fs] : [...urls].map((u) => fs.filter((f) => f.url === u)).concat([fs.filter((f) => !f.url)]);
    for (const grp of groups) {
      if (!grp.length) continue;
      const best = [...grp].sort((a, b) => RANK[a.way] - RANK[b.way] || Number(!a.url) - Number(!b.url))[0]!;
      const url = urls.size <= 1 ? ([...urls][0] ?? '') : best.url;
      const key = `${id}|${url || best.resource}`;
      const lead: Lead & { rank: number } = {
        key,
        integration: id,
        name: grp.find((f) => f.way === 'label')?.name ?? best.name,
        ways: [...grp].sort((a, b) => RANK[a.way] - RANK[b.way]).map((f) => ({ way: f.way, text: f.text, resource: f.resource })),
        rank: RANK[best.way],
      };
      if (url) lead.url = url;
      const href = grp.find((f) => f.way === 'route')?.url ?? grp.find((f) => f.way === 'label')?.url;
      if (href) lead.href = href;
      leads.set(key, lead);
    }
  }

  const read = (l: Lead) =>
    instances.some((i) => {
      if (i.integration !== l.integration) return false;
      const u = typeof i.config?.['url'] === 'string' ? origin(i.config['url'] as string) : '';
      return l.url ? u === origin(l.url) : true;
    });
  return [...leads.values()]
    .filter((l) => !dismissed.has(l.key) && !read(l))
    .sort((a, b) => a.rank - b.rank || a.name.localeCompare(b.name))
    .map(({ rank: _rank, ...l }) => l);
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
