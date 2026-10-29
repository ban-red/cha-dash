/**
 * Leads (model/leads.ts) for this viewer, and the hub-wide dismissals:
 *
 *   GET  /api/discover/dismissed          {dismissed: [key]}
 *   POST /api/discover/dismiss  {keys}  → the same
 *   POST /api/discover/restore  {keys}  → the same
 *
 * Admins and the owner only; the Discover tab and the Inbox read it.
 */
import { computed, reactive, type ComputedRef } from 'vue';
import { api } from '../lib/api';
import { leadsOf, type Lead } from '../model/leads';
import type { InstanceDetail } from '../model/integrations';
import { integrations, loadInstances } from './integrations';
import type { Live } from './live';

export const leadsState = reactive({ dismissed: new Set<string>(), loaded: false });

function take(keys: string[] | undefined): void {
  leadsState.dismissed = new Set(keys ?? []);
  leadsState.loaded = true;
}

export async function loadLeads(): Promise<void> {
  await Promise.all([
    loadInstances(),
    api<{ dismissed: string[] }>('/api/discover/dismissed')
      .then((r) => take(r.dismissed))
      .catch(() => undefined),
  ]);
}

export async function dismissLeads(keys: string[]): Promise<void> {
  take((await api<{ dismissed: string[] }>('/api/discover/dismiss', { method: 'POST', body: { keys } })).dismissed);
}

export async function restoreLeads(keys: string[]): Promise<void> {
  take((await api<{ dismissed: string[] }>('/api/discover/restore', { method: 'POST', body: { keys } })).dismissed);
}

/** The leads on offer now: recomputed as the graph and the instances change. */
export function useLeads(live: Live): ComputedRef<Lead[]> {
  return computed(() => (leadsState.loaded ? leadsOf(live.graph.value, integrations.instances as InstanceDetail[], leadsState.dismissed) : []));
}

/** Leads someone dismissed that are still around, for "offer again". */
export function useDismissedLeads(live: Live): ComputedRef<Lead[]> {
  return computed(() => (leadsState.loaded ? leadsOf(live.graph.value, integrations.instances as InstanceDetail[]).filter((l) => leadsState.dismissed.has(l.key)) : []));
}

export function resetLeads(): void {
  leadsState.dismissed = new Set();
  leadsState.loaded = false;
}
