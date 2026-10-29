/**
 * The integration catalog and instances, shared by Settings → Integrations
 * and the command bar. One copy per page (a module singleton): both live in
 * lazy chunks, and neither should fetch twice when the other just did.
 */
import { reactive } from 'vue';
import { ApiError, api } from '../lib/api';
import { listOf, type CatalogEntry, type Instance, type InstanceDetail, type TestResult } from '../model/integrations';

export interface IntegrationsState {
  catalog: CatalogEntry[];
  instances: Instance[];
  catalogStatus: 'idle' | 'loading' | 'ready' | 'error';
  instancesStatus: 'idle' | 'loading' | 'ready' | 'error';
  error: string;
}

export const integrations = reactive<IntegrationsState>({
  catalog: [],
  instances: [],
  catalogStatus: 'idle',
  instancesStatus: 'idle',
  error: '',
});

let instancesAt = 0;
let inflight: Promise<void> | undefined;
/** Bumped by every forced load, so a fetch that began before a save can't overwrite the list that follows it. */
let loadSeq = 0;

export function errorWords(err: unknown, fallback = 'The hub couldn’t do that. Try again.'): string {
  if (!(err instanceof ApiError)) return fallback;
  if (err.status === 0) return 'Can’t reach the Cha Dash hub.';
  if (err.status === 401) return 'Your session ended. Sign in again.';
  if (err.status === 403) return 'Only the owner can change integrations.';
  if (err.status === 404) return 'This hub doesn’t have that yet.';
  if (err.status === 409) return err.message ? sentence(err.message) : 'That conflicts with something that already exists.';
  if (err.status === 422 || err.status === 400) return err.message ? sentence(err.message) : 'The hub didn’t accept that.';
  if (err.status === 429) return 'Too many requests. Try again shortly.';
  return fallback;
}

const sentence = (s: string) => (s ? s[0]!.toUpperCase() + s.slice(1) + (/[.!?]$/.test(s) ? '' : '.') : s);

export async function loadCatalog(force = false): Promise<void> {
  if (!force && integrations.catalogStatus === 'ready') return;
  integrations.catalogStatus = 'loading';
  try {
    integrations.catalog = listOf<CatalogEntry>(await api('/api/integrations/catalog'), 'integrations').sort((a, b) => a.name.localeCompare(b.name));
    integrations.catalogStatus = 'ready';
  } catch (err) {
    integrations.catalogStatus = 'error';
    integrations.error = errorWords(err, 'The catalog couldn’t be loaded.');
  }
}

/** The instance list, at most every 10 s unless forced. */
export function loadInstances(force = false): Promise<void> {
  if (!force && integrations.instancesStatus === 'ready' && Date.now() - instancesAt < 10_000) return Promise.resolve();
  // A forced load waits for a fetch already under way, then asks again: that one may predate the change.
  if (inflight) return force ? inflight.then(() => loadInstances(true)) : inflight;
  if (integrations.instancesStatus !== 'ready') integrations.instancesStatus = 'loading';
  const seq = ++loadSeq;
  const p = (async () => {
    try {
      const list = listOf<Instance>(await api('/api/integrations/instances'), 'instances').sort((a, b) => a.name.localeCompare(b.name));
      if (seq === loadSeq) {
        integrations.instances = list;
        integrations.instancesStatus = 'ready';
        instancesAt = Date.now();
      }
    } catch (err) {
      integrations.instancesStatus = 'error';
      integrations.error = errorWords(err, 'Integrations couldn’t be loaded.');
    } finally {
      if (seq === loadSeq) inflight = undefined;
    }
  })();
  inflight = p;
  return p;
}

/** A bound tile is named after its integration instance; undefined when it isn't bound or the list hasn't it (a viewer who can't list them). */
export const instanceName = (id: string | undefined): string | undefined => (id ? integrations.instances.find((x) => x.id === id)?.name : undefined);

export const catalogEntry = (id: string) => integrations.catalog.find((c) => c.id === id);

export async function loadInstance(id: string): Promise<InstanceDetail> {
  return api<InstanceDetail>(`/api/integrations/instances/${encodeURIComponent(id)}`);
}

export interface InstanceInput {
  integration?: string;
  name?: string;
  config?: unknown;
  secrets?: Record<string, string>;
}

export async function createInstance(body: Required<Pick<InstanceInput, 'integration' | 'name' | 'config'>> & { secrets: Record<string, string> }): Promise<string> {
  const res = await api<{ id: string }>('/api/integrations/instances', { method: 'POST', body });
  void loadInstances(true);
  return res.id;
}

export async function updateInstance(id: string, body: InstanceInput): Promise<void> {
  await api(`/api/integrations/instances/${encodeURIComponent(id)}`, { method: 'PUT', body });
  // Show the new name at once; the fresh list confirms it.
  const known = integrations.instances.find((x) => x.id === id);
  if (known && body.name) known.name = body.name;
  await loadInstances(true);
}

export async function deleteInstance(id: string): Promise<void> {
  await api(`/api/integrations/instances/${encodeURIComponent(id)}`, { method: 'DELETE' });
  integrations.instances = integrations.instances.filter((x) => x.id !== id);
  void loadInstances(true);
}

/** Test unsaved settings, or a saved instance as stored. */
/**
 * Test a connection: a saved instance as stored ({id}), unsaved edits to one
 * with its stored secrets filled in by the hub ({instance_id}), or a new one.
 */
export function testConnection(
  target: { id: string } | { instance_id: string; config: unknown; secrets: Record<string, string> } | { integration: string; config: unknown; secrets: Record<string, string> },
): Promise<TestResult> {
  if ('id' in target) return api<TestResult>(`/api/integrations/instances/${encodeURIComponent(target.id)}/test`, { method: 'POST' });
  return api<TestResult>('/api/integrations/test', { method: 'POST', body: target });
}

/** A started sign-in (POST /api/integrations/authorize/{integration}). */
export interface AuthStart {
  id: string;
  code: string;
  url: string;
  expires_at: string;
  interval_s: number;
}

/** A sign-in's progress. `secrets` come once, with `done`, and go only into the form. */
export interface AuthStatus {
  status: 'pending' | 'done' | 'expired' | 'failed';
  expires_at?: string;
  secrets?: Record<string, string>;
  error?: { code: string; message: string };
}

/** Start an integration's sign-in flow (Plex's PIN). */
export function beginAuthorize(integration: string): Promise<AuthStart> {
  return api<AuthStart>(`/api/integrations/authorize/${encodeURIComponent(integration)}`, { method: 'POST' });
}

/** Check on a sign-in. */
export function pollAuthorize(integration: string, id: string): Promise<AuthStatus> {
  return api<AuthStatus>(`/api/integrations/authorize/${encodeURIComponent(integration)}/${encodeURIComponent(id)}`);
}

/** For tests. */
export function resetIntegrations(): void {
  Object.assign(integrations, { catalog: [], instances: [], catalogStatus: 'idle', instancesStatus: 'idle', error: '' });
  instancesAt = 0;
  inflight = undefined;
}
