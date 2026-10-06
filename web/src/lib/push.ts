/**
 * "Notify me on this device": Web Push through the service worker
 * (internal/notify). Permission is only ever asked from a click.
 *
 *   GET    /api/notify/push/key            {public_key}: the hub's VAPID key, base64url
 *   GET    /api/notify/push/subscriptions  {subscriptions}: your devices
 *   POST   /api/notify/push/subscriptions  PushSubscription.toJSON() → 201 device
 *   DELETE /api/notify/push/subscriptions/{id}
 *   POST   /api/notify/push/unsubscribe    {endpoint}
 *
 * Push needs a secure context (HTTPS, or localhost), a service worker (only
 * registered in a production build) and the Push API; iOS adds it to a
 * Home Screen app only. Each case gets a sentence instead of a button that
 * can't work.
 */
import { api } from './api';

export interface PushDevice {
  id: string;
  label: string;
  host: string;
  fingerprint: string;
  created_at: string;
  last_ok_at: string | null;
  last_error: string | null;
}

export type PushSupport = { ok: true } | { ok: false; reason: string };

export function pushSupport(): PushSupport {
  if (typeof window === 'undefined' || typeof navigator === 'undefined') return { ok: false, reason: 'This browser can’t receive notifications.' };
  if (!window.isSecureContext) {
    return { ok: false, reason: 'Browsers only allow notifications over HTTPS (or on localhost). Open Cha Dash at its https:// address to turn them on.' };
  }
  if (!('serviceWorker' in navigator) || !('PushManager' in window) || !('Notification' in window)) {
    return { ok: false, reason: 'This browser can’t receive push notifications. On an iPhone or iPad, add Cha Dash to the Home Screen first, then open it from there.' };
  }
  return { ok: true };
}

/** The browser's permission, or 'unsupported'. */
export function permission(): NotificationPermission | 'unsupported' {
  return typeof Notification === 'undefined' ? 'unsupported' : Notification.permission;
}

const b64urlToBytes = (s: string): Uint8Array<ArrayBuffer> => {
  const pad = '='.repeat((4 - (s.length % 4)) % 4);
  const bin = atob((s + pad).replace(/-/g, '+').replace(/_/g, '/'));
  const out = new Uint8Array(new ArrayBuffer(bin.length));
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
};

/** The first 16 hex digits of the endpoint's SHA-256: how the hub's list names this device. */
export async function fingerprint(endpoint: string): Promise<string> {
  const sum = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(endpoint));
  return Array.from(new Uint8Array(sum).slice(0, 8), (b) => b.toString(16).padStart(2, '0')).join('');
}

async function registration(): Promise<ServiceWorkerRegistration> {
  const reg = await Promise.race([
    navigator.serviceWorker.ready,
    new Promise<never>((_, reject) => setTimeout(() => reject(new Error('Cha Dash’s service worker isn’t running here, so notifications can’t be turned on. Reload the page and try again.')), 8000)),
  ]);
  return reg;
}

/** This browser's push subscription, if any. */
export async function currentSubscription(): Promise<PushSubscription | null> {
  if (!pushSupport().ok) return null;
  try {
    const reg = await navigator.serviceWorker.getRegistration();
    return (await reg?.pushManager.getSubscription()) ?? null;
  } catch {
    return null;
  }
}

/**
 * Turn notifications on for this device. Call it from a click: it asks for
 * permission, subscribes with the hub's key and registers the device.
 */
export async function enablePush(): Promise<PushDevice> {
  const support = pushSupport();
  if (!support.ok) throw new Error(support.reason);
  const granted = Notification.permission === 'granted' ? 'granted' : await Notification.requestPermission();
  if (granted !== 'granted') {
    throw new Error(
      granted === 'denied'
        ? 'Notifications are blocked for Cha Dash in this browser. Allow them in the site’s settings (the icon beside the address), then try again.'
        : 'Notifications weren’t allowed, so nothing changed.',
    );
  }
  const { public_key } = await api<{ public_key: string }>('/api/notify/push/key');
  const reg = await registration();
  let sub = await reg.pushManager.getSubscription();
  const key = b64urlToBytes(public_key);
  // A subscription made with another key (the hub's data was reset) can't be used.
  if (sub && sub.options.applicationServerKey && !sameBytes(new Uint8Array(sub.options.applicationServerKey), key)) {
    await sub.unsubscribe().catch(() => undefined);
    sub = null;
  }
  sub ??= await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key });
  return api<PushDevice>('/api/notify/push/subscriptions', { method: 'POST', body: sub.toJSON() });
}

/** Turn notifications off for this device: the hub forgets it, then the browser does. */
export async function disablePush(): Promise<void> {
  const sub = await currentSubscription();
  if (!sub) return;
  try {
    await api('/api/notify/push/unsubscribe', { method: 'POST', body: { endpoint: sub.endpoint } });
  } catch (err) {
    if ((err as { status?: number }).status !== 404) throw err;
  }
  await sub.unsubscribe().catch(() => undefined);
}

export const listDevices = async (): Promise<PushDevice[]> => (await api<{ subscriptions?: PushDevice[] }>('/api/notify/push/subscriptions'))?.subscriptions ?? [];

export const removeDevice = (id: string): Promise<void> => api(`/api/notify/push/subscriptions/${encodeURIComponent(id)}`, { method: 'DELETE' });

function sameBytes(a: Uint8Array, b: Uint8Array): boolean {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false;
  return true;
}
