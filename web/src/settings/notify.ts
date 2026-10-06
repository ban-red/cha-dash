/**
 * Settings → Notifications: the hub's shapes and calls (internal/notify).
 *
 *   GET    /api/notify/channels                  {channels, public_url, web_push}
 *   POST   /api/notify/channels                  {kind, name, config, secrets, enabled?} → channel
 *   PUT    /api/notify/channels/{id}             {name?, config?, secrets?, enabled?} → channel
 *   DELETE /api/notify/channels/{id}
 *   POST   /api/notify/channels/{id}/test        {ok, status_code, error, devices?}
 *   GET    /api/notify/rules                     {rules}
 *   PUT    /api/notify/rules/{id}                {enabled?, resolved?, grace_minutes?, channels?} → rule
 *   GET    /api/notify/deliveries?state=&limit=  {deliveries}
 *
 * Secrets go in once and never come back: a channel says only whether each
 * is set.
 */
import { api } from '../lib/api';

export type ChannelKind = 'webpush' | 'ntfy' | 'webhook';

export interface NotifyChannel {
  id: string;
  kind: ChannelKind;
  name: string;
  enabled: boolean;
  builtin: boolean;
  config: { server?: string; topic?: string; url?: string };
  secrets: Record<string, { set: boolean }>;
  created_at: string;
  updated_at: string;
  last: { state: string; at: string; error: string | null } | null;
  devices?: number;
}

export interface RuleSettings {
  enabled: boolean;
  resolved: boolean;
  grace_minutes?: number;
  channels: string[] | null;
}

export interface NotifyRule extends RuleSettings {
  id: string;
  label: string;
  description: string;
  locked?: boolean;
  has_resolved: boolean;
  defaults: RuleSettings;
  updated_at: string | null;
  updated_by?: string;
}

export interface NotifyDelivery {
  id: string;
  channel_id: string;
  channel_name: string;
  channel_kind: ChannelKind;
  device: string | null;
  kind: string;
  title: string;
  state: 'pending' | 'sent' | 'failed' | 'dropped' | (string & {});
  attempts: number;
  status_code: number | null;
  error: string | null;
  created_at: string;
  updated_at: string;
  next_at: string | null;
}

export interface TestResult {
  ok: boolean;
  status_code: number | null;
  error: string | null;
  devices?: number;
}

export interface ChannelInput {
  kind?: ChannelKind;
  name?: string;
  enabled?: boolean;
  config?: Record<string, string>;
  secrets?: Record<string, string | null>;
}

export const listChannels = () => api<{ channels: NotifyChannel[]; public_url: boolean; web_push: boolean }>('/api/notify/channels');
export const createChannel = (body: ChannelInput) => api<NotifyChannel>('/api/notify/channels', { method: 'POST', body });
export const updateChannel = (id: string, body: ChannelInput) => api<NotifyChannel>(`/api/notify/channels/${encodeURIComponent(id)}`, { method: 'PUT', body });
export const deleteChannel = (id: string) => api<void>(`/api/notify/channels/${encodeURIComponent(id)}`, { method: 'DELETE' });
export const testChannel = (id: string) => api<TestResult>(`/api/notify/channels/${encodeURIComponent(id)}/test`, { method: 'POST' });
export const listRules = async () => (await api<{ rules?: NotifyRule[] }>('/api/notify/rules'))?.rules ?? [];
export const updateRule = (id: string, body: Partial<RuleSettings>) => api<NotifyRule>(`/api/notify/rules/${encodeURIComponent(id)}`, { method: 'PUT', body });
export const listDeliveries = async (state = '') =>
  (await api<{ deliveries?: NotifyDelivery[] }>(`/api/notify/deliveries?limit=50${state ? `&state=${encodeURIComponent(state)}` : ''}`))?.deliveries ?? [];

export const KIND_WORDS: Record<ChannelKind, string> = { webpush: 'Web Push', ntfy: 'ntfy', webhook: 'Webhook' };

/** Where a channel sends, in a few words. */
export function channelTarget(c: NotifyChannel): string {
  if (c.kind === 'webpush') return c.devices === 1 ? '1 device' : `${c.devices ?? 0} devices`;
  if (c.kind === 'ntfy') return `${hostOf(c.config.server)} · ${c.config.topic ?? ''}`;
  return hostOf(c.config.url);
}

function hostOf(u: string | undefined): string {
  try {
    return u ? new URL(u).host : '';
  } catch {
    return u ?? '';
  }
}

/** A fresh signing secret for a webhook: 32 random bytes, base64url. */
export function generateSecret(): string {
  const b = crypto.getRandomValues(new Uint8Array(32));
  return 'whsec_' + btoa(String.fromCharCode(...b)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

export const TOPIC_RE = /^[A-Za-z0-9_-]{1,64}$/;

/** "" when an http(s) URL is usable, else why not. */
export function urlProblem(raw: string): string {
  if (!raw.trim()) return 'Required.';
  try {
    const u = new URL(raw.trim());
    if (u.protocol !== 'http:' && u.protocol !== 'https:') return 'An http:// or https:// address.';
    if (u.username || u.password) return 'Put credentials in the token or secret field, not the address.';
    return '';
  } catch {
    return 'An http:// or https:// address.';
  }
}
