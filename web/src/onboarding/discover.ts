/**
 * Discovery for the wizard (internal/onboarding):
 *
 *   POST /api/onboarding/discover {sidecar?, docker?, hosts?} → {found, checked}
 *
 * The hub probes only what's in the request: the compose sidecar's socket
 * proxy, Docker endpoints the owner ticked, and machines the owner typed in.
 * GET requests only, a handful of targets at a time, never a range or a
 * subnet. These helpers keep the browser side to the same rules, so a typo
 * is caught before it's sent.
 */
import { api } from '../lib/api';
import type { Grant, Warning } from '../model/integrations';

export interface Candidate {
  integration: string;
  name: string;
  config: Record<string, unknown>;
  source: 'sidecar' | 'docker' | 'host' | string;
  target: string;
  /** Needs no credential: one click adds it. */
  ready: boolean;
  version?: string;
  /** The instance that already reads it. */
  added?: string;
  grants: Grant[];
  warnings: Warning[];
}

export interface Checked {
  target: string;
  source: string;
  found: number;
  error?: string;
}

export interface DiscoverResult {
  found: Candidate[];
  checked: Checked[];
}

export interface DiscoverRequest {
  sidecar?: boolean;
  docker?: string[];
  hosts?: string[];
}

export const MAX_HOSTS = 8;

export function discover(req: DiscoverRequest): Promise<DiscoverResult> {
  return api<DiscoverResult>('/api/onboarding/discover', { method: 'POST', body: req });
}

/** Docker endpoints people commonly have, offered as boxes to tick; none is checked unless ticked. */
export const WELL_KNOWN_DOCKER: readonly { endpoint: string; note: string }[] = [
  { endpoint: 'tcp://dockerproxy:2375', note: 'the socket proxy name Homepage’s guide uses' },
  { endpoint: 'tcp://docker-socket-proxy:2375', note: 'another common socket proxy name' },
  { endpoint: 'tcp://host.docker.internal:2375', note: 'this machine’s Docker API, if it listens on 2375' },
  { endpoint: 'unix:///var/run/docker.sock', note: 'the Docker socket, if it’s mounted into Cha Dash' },
];

const HOST = /^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$/;
const IPV4 = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/;
const IPV6 = /^[0-9A-Fa-f:]+(\.\d{1,3}){0,4}$/;

/** One machine: a name or an address. No scheme, port, range, wildcard or subnet. */
export function validHost(h: string): boolean {
  if (h.length > 253) return false;
  const v4 = IPV4.exec(h);
  if (v4) return v4.slice(1).every((n) => Number(n) <= 255);
  if (h.includes(':')) return IPV6.test(h) && h.split(':').length >= 3;
  // A name's last label has a letter (no top-level domain is all digits), so
  // "198.51.100.1-50" reads as the range it is, not as a name.
  return HOST.test(h) && /[A-Za-z]/.test(h.split('.').pop() ?? '');
}

/** The machines in a text box: one per line (or separated by spaces or commas), and the ones that won't do. */
export function parseHosts(text: string): { hosts: string[]; bad: string[] } {
  const hosts: string[] = [];
  const bad: string[] = [];
  for (const raw of text.split(/[\s,]+/)) {
    const h = raw.trim();
    if (!h || hosts.includes(h) || bad.includes(h)) continue;
    (validHost(h) ? hosts : bad).push(h);
  }
  return { hosts, bad };
}

/** What a candidate is reached at: its url or endpoint. */
export function targetOf(c: Pick<Candidate, 'config'>): string {
  const u = c.config['url'] ?? c.config['endpoint'];
  return typeof u === 'string' ? u : '';
}

export const candidateKey = (c: Candidate) => `${c.integration}|${targetOf(c)}`;

/** Remembered for the page's life, so stepping back and forth doesn't probe again. */
export const cache: { sidecar?: DiscoverResult } = {};
