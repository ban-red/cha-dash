/**
 * Page-level UI state: the Inbox sheet, the hold-to-confirm dialog and toasts.
 * Also the one place that runs an incident's suggested action through the
 * safety ladder (T0/T1 run now, T2 holds to confirm, T3+ aren't offered yet).
 */
import { inject, provide, reactive, type InjectionKey } from 'vue';
import type { Action, Incident } from '../model/types';
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

export function createUi(live: Live) {
  const state = reactive({
    inboxOpen: false,
    /** The incident the Inbox opens on, expanded. */
    inboxFocus: '' as string,
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
    state.inboxFocus = focus;
    state.inboxOpen = true;
  }

  function closeInbox(): void {
    state.inboxOpen = false;
  }

  /** Run an incident's suggested action through the safety ladder. */
  function act(incident: Incident): void {
    const action = incident.suggested;
    if (!action) return;
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

  return { state, toast, dismiss, openInbox, closeInbox, act, confirmed, cancelConfirm };
}

export type Ui = ReturnType<typeof createUi>;

const KEY: InjectionKey<Ui> = Symbol('ui');
export const provideUi = (u: Ui) => provide(KEY, u);
export function useUi(): Ui {
  const u = inject(KEY);
  if (!u) throw new Error('useUi() outside the app');
  return u;
}
