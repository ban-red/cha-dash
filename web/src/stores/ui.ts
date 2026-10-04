/**
 * Page-level UI state: the Inbox sheet, the command bar, edit mode, the
 * hold-to-confirm dialog and toasts. Also the one place that runs an
 * incident's actions through the safety ladder:
 *
 * - In demo mode (GET /api/demo), a suggested action runs through the demo
 *   Fix endpoint: T0 and T1 straight away, T2 behind the hold-to-confirm ring.
 * - Everywhere, `link.open` (T0) opens the target's link.
 * - Anything else waits for the action engine, and says so.
 * - A Repair's "Reconnect" opens the integration's settings with the field
 *   that failed focused.
 */
import { inject, provide, reactive, type InjectionKey } from 'vue';
import { navigate, settingsPath, type SettingsPane } from '../lib/router';
import { prefersReducedMotion } from '../lib/motion';
import { repairTarget } from '../model/integrations';
import { label } from '../model/graph';
import { resourceLinks, type Action, type Incident } from '../model/types';
import type { Live } from './live';

export interface Toast {
  id: number;
  text: string;
  tone?: 'ok' | 'error';
}

export interface ConfirmRequest {
  incident: Incident;
  action: Action;
}

export interface UiHooks {
  /** Stop the socket and sign out (set by the signed-in shell). */
  signOut?: () => Promise<void> | void;
}

export interface Runnable {
  ok: boolean;
  /** Why not, for the button's title. */
  why?: string;
}

const safeUrl = (u: string | undefined) => (u && /^https?:\/\//i.test(u) ? u : undefined);

export function createUi(live: Live, hooks: UiHooks = {}) {
  const state = reactive({
    inboxOpen: false,
    /** The incident the Inbox opens on, expanded. */
    inboxFocus: '' as string,
    paletteOpen: false,
    /** The board is in edit mode (EditableBoard). */
    editing: false,
    confirm: undefined as ConfirmRequest | undefined,
    toasts: [] as Toast[],
  });
  let nextToast = 1;

  function toast(text: string, tone: Toast['tone'] = 'ok', ms = 5000): void {
    const t: Toast = { id: nextToast++, text, tone };
    state.toasts.push(t);
    if (state.toasts.length > 3) state.toasts.shift();
    setTimeout(() => dismiss(t.id), ms);
  }

  function dismiss(id: number): void {
    const i = state.toasts.findIndex((t) => t.id === id);
    if (i >= 0) state.toasts.splice(i, 1);
  }

  function openInbox(focus = ''): void {
    state.paletteOpen = false;
    state.inboxFocus = focus;
    state.inboxOpen = true;
  }

  function closeInbox(): void {
    state.inboxOpen = false;
  }

  function openPalette(): void {
    state.paletteOpen = true;
  }

  function closePalette(): void {
    state.paletteOpen = false;
  }

  /** Open a settings pane over the front page (closing the Inbox and the command bar). */
  function openSettings(pane: SettingsPane = 'integrations', id?: string, query?: Record<string, string | undefined>): void {
    state.inboxOpen = false;
    state.paletteOpen = false;
    navigate(settingsPath(pane, id, query));
  }

  /** A Repair's Reconnect: the integration's settings, the failing field focused. */
  function reconnect(incident: Incident): void {
    const t = repairTarget(incident);
    openSettings('integrations', t.ref, { field: t.field, reason: t.reason });
  }

  /** The first http(s) link of a resource, if it has one. */
  function linkOf(resourceId: string): string | undefined {
    const r = live.graph.value?.resources[resourceId];
    return safeUrl(resourceLinks(r)[0]?.url);
  }

  /** Can this incident's action run here, now? */
  function canRun(incident: Incident, action: Action | undefined = incident.suggested): Runnable {
    if (!action) return { ok: false };
    if (live.stale.value) return { ok: false, why: 'Actions come back when the hub does.' };
    if (action.id === 'link.open') return linkOf(action.target) ? { ok: true } : { ok: false, why: 'No link known for this yet.' };
    if (action.tier === 'T3' || action.tier === 'T4') return { ok: false, why: 'Type-to-confirm arrives with the action engine.' };
    if (!live.demo.value) return { ok: false, why: 'Cha Dash v0.1 shows what it would do; running actions arrives with the action engine.' };
    if (action !== incident.suggested) return { ok: false, why: 'Only the suggested action runs in this version.' };
    if (live.fixes[incident.id]?.state === 'sending') return { ok: false };
    return { ok: true };
  }

  /** Run an action through the safety ladder. */
  function act(incident: Incident, action: Action | undefined = incident.suggested): void {
    if (!action) return;
    if (action.id === 'link.open') {
      const url = linkOf(action.target);
      if (url) window.open(url, '_blank', 'noopener,noreferrer');
      return;
    }
    if (!live.demo.value) {
      toast('Running actions arrives with the action engine. Until then, this is what Cha Dash would do.', 'error');
      return;
    }
    if (action !== incident.suggested) return; // the demo endpoint runs the suggested action only
    if (action.tier === 'T0' || action.tier === 'T1') {
      void run(incident, action);
    } else if (action.tier === 'T2') {
      state.confirm = { incident, action };
    } else {
      toast(`${action.label} needs type-to-confirm, which arrives with the action engine.`, 'error');
    }
  }

  async function run(incident: Incident, action: Action): Promise<void> {
    try {
      await live.fix(incident.id);
      toast(`Started: ${action.label}.`);
    } catch {
      toast(live.fixes[incident.id]?.error ?? 'That didn’t start. Try again.', 'error');
    }
  }

  function confirmed(): void {
    const c = state.confirm;
    state.confirm = undefined;
    if (c) void run(c.incident, c.action);
  }

  function cancelConfirm(): void {
    state.confirm = undefined;
  }

  /**
   * Show a resource (the command bar's "open"): its open incident in the
   * Inbox, else its tile on the board, else its link.
   */
  function reveal(resourceId: string): void {
    const incident = Object.values(live.inbox.value?.incidents ?? {}).find((i) => i.status === 'open' && i.target === resourceId);
    if (incident) return openInbox(incident.id);
    const cell = typeof document !== 'undefined' ? [...document.querySelectorAll<HTMLElement>('[data-item]')].find((el) => el.dataset['item']?.split('@')[1] === resourceId) : undefined;
    if (cell) {
      cell.scrollIntoView({ block: 'center', behavior: prefersReducedMotion() ? 'auto' : 'smooth' });
      cell.classList.remove('flash');
      void cell.offsetWidth; // restart the animation
      cell.classList.add('flash');
      setTimeout(() => cell.classList.remove('flash'), 1600);
      cell.querySelector<HTMLElement>('[tabindex], a, button')?.focus({ preventScroll: true });
      return;
    }
    const url = linkOf(resourceId);
    if (url) {
      window.open(url, '_blank', 'noopener,noreferrer');
      return;
    }
    const name = label(live.graph.value?.resources[resourceId]) || resourceId;
    toast(`${name} isn’t on this board, and has no link yet.`);
  }

  async function signOut(): Promise<void> {
    state.paletteOpen = false;
    await hooks.signOut?.();
  }

  return {
    state,
    toast,
    dismiss,
    openInbox,
    closeInbox,
    openPalette,
    closePalette,
    openSettings,
    reconnect,
    canRun,
    act,
    confirmed,
    cancelConfirm,
    reveal,
    signOut,
  };
}

export type Ui = ReturnType<typeof createUi>;

const KEY: InjectionKey<Ui> = Symbol('ui');
export const provideUi = (u: Ui) => provide(KEY, u);
export function useUi(): Ui {
  const u = inject(KEY);
  if (!u) throw new Error('useUi() outside the app');
  return u;
}
