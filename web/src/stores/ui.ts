/**
 * Page-level UI state: the Inbox sheet, the command bar, edit mode, the
 * confirm and step-up dialogs and toasts. Also the one place that runs an
 * incident's actions through the safety ladder (stores/actions.ts):
 *
 * - Every action is planned by the hub's action engine, and the plan's
 *   `confirm` picks the rung: run, run with Undo, hold, or type the name.
 *   Step-up runs first when the plan asks for it. Demo mode goes the same
 *   way: the Demo Home's fixes are engine actions.
 * - Availability comes from the hub: an action's `unavailable` sentence (or
 *   `plan_ready: false`) disables it, and planning refuses with a sentence.
 *   The page only hides what the viewer's role can't use (model/roles.ts).
 * - Everywhere, `link.open` (T0) opens the target's link. Outside the Demo
 *   Home, `integration.settings` (T0) opens the target instance's settings.
 *   `integration.refresh` (T0, Check now) asks the instance to sync now.
 * - A Repair's "Reconnect" opens the integration's settings with the field
 *   that failed focused.
 * - A tile's cog opens its settings: the integration that feeds it, else
 *   the tile's editor over the board (`state.editingTile`), without edit mode.
 * - A task whose log is its result (a container's logs) ends in the log
 *   dialog (TaskLogDialog), not a toast.
 * - A note too long for its tile (an upstream's health message) opens whole
 *   in NoteDialog.
 * - Home Assistant's tile opens its panel (HomeAssistantPanel) on a list.
 * - A resource's sheet follows the route (`?r=<id>`): opening one pushes a
 *   history entry, so Back closes it or returns to the sheet before.
 * - A suggestion's answer (Merge or Keep separate) goes to the hub's
 *   correlator, which remembers it.
 */
import { computed, inject, provide, reactive, type InjectionKey } from 'vue';
import { ApiError, api } from '../lib/api';
import { navigate, route, settingsPath, sheetOf, sheetPath, type SettingsPane } from '../lib/router';
import { prefersReducedMotion } from '../lib/motion';
import { repairTarget } from '../model/integrations';
import { label } from '../model/graph';
import { permissions } from '../model/roles';
import { hasSheet } from '../model/sheets';
import { resourceLinks, safeUrl, type Action, type Incident } from '../model/types';
import { createActions, type ActionRequest, type ConfirmRequest, type StepUpRequest, type TaskLog, type ToastAction } from './actions';
import type { Live } from './live';
import type { GlyphKind } from '../ui/marks/Glyph.vue';
import type { AddRequest } from '../model/edit';

export type { ConfirmRequest, TaskLog } from './actions';

export interface Toast {
  id: number;
  text: string;
  tone?: 'ok' | 'error';
  /** A button in the toast: Undo, or "Open its settings". */
  action?: ToastAction;
}

/** A note in full: whose it is, and its words. */
export interface NoteView {
  title: string;
  text: string;
  glyph?: GlyphKind;
  /** The upstream's page for it: the dialog offers to open it. */
  href?: string;
}

export interface UiHooks {
  /** Stop the socket and sign out (set by the signed-in shell). */
  signOut?: () => Promise<void> | void;
  /** The signed-in viewer's role (session.state.user.role). */
  role?: () => string | undefined;
  /** Poll interval for tasks on a hub without the `tasks` topic (tests shorten it). */
  pollMs?: number;
}

export interface Runnable {
  ok: boolean;
  /** Why not, for the button's title. */
  why?: string;
  /** The viewer's role can't run it: don't show the button at all. */
  hidden?: boolean;
}

export function createUi(live: Live, hooks: UiHooks = {}) {
  const state = reactive({
    inboxOpen: false,
    /** The incident the Inbox opens on, expanded. */
    inboxFocus: '' as string,
    paletteOpen: false,
    /** The board is in edit mode (EditableBoard). */
    editing: false,
    /** ⌘K asked for the widget picker: edit mode opens it, then clears this. */
    addingWidget: false,
    /** With addingWidget: the section whose header asked, so the picker opens on it. */
    addSection: '' as string,
    /** A widget picked on the normal board: edit mode starts with it added (EditableBoard), then clears this. */
    pendingAdd: undefined as AddRequest | undefined,
    /** A tile's cog asked for its Edit: the board opens the tile editor on this item (over the normal board, or in edit mode), then clears this. */
    editingTile: '' as string,
    confirm: undefined as ConfirmRequest | undefined,
    stepUp: undefined as StepUpRequest | undefined,
    toasts: [] as Toast[],
    /** The finished task's log the log dialog shows. */
    taskLog: undefined as TaskLog | undefined,
    /** A tile note shown whole (NoteDialog). */
    note: undefined as NoteView | undefined,
    /** Home Assistant's panel (HomeAssistantPanel): the app it shows, and on which list. */
    homePanel: undefined as { app: string; tab: string } | undefined,
  });
  let nextToast = 1;
  /** What the viewer's role may see and do. The hub enforces it; this only hides. */
  const can = computed(() => permissions(hooks.role?.()));

  function toast(text: string, tone: Toast['tone'] = 'ok', ms = 5000, action?: ToastAction): void {
    const t: Toast = { id: nextToast++, text, tone };
    if (action) t.action = action;
    state.toasts.push(t);
    if (state.toasts.length > 3) state.toasts.shift();
    setTimeout(() => dismiss(t.id), ms);
  }

  function dismiss(id: number): void {
    const i = state.toasts.findIndex((t) => t.id === id);
    if (i >= 0) state.toasts.splice(i, 1);
  }

  /** The resource whose sheet is open, from the route. */
  const sheet = computed(() => sheetOf(route.value));

  /** Open a resource's sheet over the front page (closing the Inbox and the command bar). */
  function openSheet(id: string): void {
    state.paletteOpen = false;
    state.inboxOpen = false;
    if (sheet.value !== id) navigate(sheetPath(route.value, id));
  }

  function closeSheet(): void {
    if (sheet.value) navigate(sheetPath(route.value, undefined), { replace: true });
  }

  function openInbox(focus = ''): void {
    if (!can.value.inbox) return;
    closeSheet();
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
    if (!can.value.settings || !can.value.panes.includes(pane)) return;
    state.inboxOpen = false;
    state.paletteOpen = false;
    navigate(settingsPath(pane, id, query));
  }

  /** A Repair's Reconnect: the integration's settings, the failing field focused. */
  function reconnect(incident: Incident): void {
    const t = repairTarget(incident);
    openSettings('integrations', t.ref, { field: t.field, reason: t.reason });
  }

  /**
   * Where a tile's cog goes: the settings of the integration feeding it when
   * the viewer may open them, else the tile's Edit when they may edit boards.
   */
  function tileSettings(instance: string | undefined): 'integration' | 'tile' | undefined {
    if (instance && can.value.integrations) return 'integration';
    if (can.value.editBoards) return 'tile';
    return undefined;
  }

  function openTileSettings(itemId: string, instance: string | undefined): void {
    const where = tileSettings(instance);
    if (where === 'integration') openSettings('integrations', instance);
    else if (where === 'tile' && itemId) {
      closeSheet();
      state.inboxOpen = false;
      state.paletteOpen = false;
      // The editor opens over the board as it is (BoardSurface); edit mode isn't switched on.
      state.editingTile = itemId;
    }
  }

  /** The first http(s) link of a resource, if it has one. */
  function linkOf(resourceId: string): string | undefined {
    const r = live.graph.value?.resources[resourceId];
    return safeUrl(resourceLinks(r)[0]?.url);
  }

  const actions = createActions({
    live,
    state,
    toast,
    showLog: (log) => {
      state.paletteOpen = false;
      state.taskLog = log;
    },
    // An action refused as unavailable: the instance's "Let Cha Dash act" section.
    openRecipe: (instance) => openSettings('integrations', instance, { section: 'control' }),
    canOpenRecipe: () => can.value.integrations,
    ...(hooks.pollMs !== undefined ? { pollMs: hooks.pollMs } : {}),
  });

  /**
   * `integration.settings` opens the target instance's settings. Only the
   * Demo Home registers it as an action (there it settles the incident); a
   * real hub would refuse it as unknown.
   */
  function opensSettings(action: Action): boolean {
    return action.id === 'integration.settings' && !live.demo.value;
  }

  /**
   * `integration.refresh` (Check now) asks the instance to read its source
   * now. The result arrives live; the hub answers before the check ends.
   */
  async function refresh(target: string): Promise<void> {
    try {
      await api(`/api/integrations/instances/${encodeURIComponent(target.replace(/^integration:/, ''))}/refresh`, { method: 'POST' });
      toast('Checking now. This updates by itself.');
    } catch (e) {
      toast(e instanceof ApiError && e.code === 'not_running' ? 'That integration isn’t running, so it can’t check now.' : 'Couldn’t ask for a check. Try again.', 'error');
    }
  }

  /** Can this incident's action run here, now? */
  function canRun(incident: Incident, action: Action | undefined = incident.suggested): Runnable {
    if (!action) return { ok: false };
    if (!can.value.act(action.tier, action.household)) return { ok: false, hidden: true, why: `Your role can’t run ${action.tier} actions.` };
    if (live.stale.value) return { ok: false, why: 'Actions come back when the hub does.' };
    if (action.id === 'link.open') return linkOf(action.target) ? { ok: true } : { ok: false, why: 'No link known for this yet.' };
    if (opensSettings(action)) return can.value.integrations ? { ok: true } : { ok: false, hidden: true, why: 'Your role can’t open integration settings.' };
    // The route is viewer and up: household's boards don't ask sources to read.
    if (action.id === 'integration.refresh') return can.value.role === 'household' ? { ok: false, hidden: true } : { ok: true };
    if (action.unavailable) return { ok: false, why: action.unavailable };
    if (action.plan_ready === false) return { ok: false, why: 'Cha Dash can’t run this here yet.' };
    if (live.fixes[incident.id]?.state === 'sending') return { ok: false };
    return { ok: true };
  }

  /** Run an incident's action through the safety ladder. */
  function act(incident: Incident, action: Action | undefined = incident.suggested): void {
    if (!action) return;
    if (action.id === 'link.open') {
      const url = linkOf(action.target);
      if (url) window.open(url, '_blank', 'noopener,noreferrer');
      return;
    }
    if (opensSettings(action)) {
      if (can.value.integrations) openSettings('integrations', action.target.replace(/^integration:/, ''));
      return;
    }
    if (action.id === 'integration.refresh') {
      void refresh(action.target);
      return;
    }
    const r = canRun(incident, action);
    if (!r.ok) {
      if (r.why) toast(r.why, 'error');
      return;
    }
    const req: ActionRequest = { action: action.id, target: action.target, label: action.label, tier: action.tier, incident };
    if (action.params) req.params = action.params;
    if (action.detail) req.detail = action.detail;
    const impact = action.impact ?? incident.blast_radius;
    if (impact) req.impact = impact;
    void actions.run(req);
  }

  /** The confirm dialog's answer: the ring filled, the name was typed, or Run. */
  function confirmed(typed?: string): void {
    void actions.confirmed(typed);
  }

  function cancelConfirm(): void {
    actions.cancel();
  }

  /**
   * Answer one of the correlator's suggestions: merge (or link) the two
   * things, or keep them separate. The hub remembers the answer, and the
   * item resolves live on the `inbox` topic. Resolves true on success.
   */
  async function answer(incident: Incident, verdict: 'confirm' | 'reject'): Promise<boolean> {
    try {
      await api(`/api/correlate/suggestions/${encodeURIComponent(incident.id)}/${verdict}`, { method: 'POST' });
      if (verdict === 'reject') toast('Kept separate. Cha Dash won’t suggest this again.');
      else toast(incident.scenario === 'merge-suggestion' ? 'Merged. Cha Dash shows them as one.' : 'Linked.');
      return true;
    } catch (err) {
      toast(err instanceof ApiError && err.status === 404 ? 'That was already answered, or no longer applies.' : 'Couldn’t save your answer. Try again.', 'error');
      return false;
    }
  }

  /**
   * Show a resource (the command bar's "open"): its open incident in the
   * Inbox, else its tile on the board, else its link.
   */
  function reveal(resourceId: string): void {
    const incident = can.value.inbox
      ? Object.values(live.inbox.value?.incidents ?? {}).find((i) => i.status === 'open' && i.target === resourceId)
      : undefined;
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
    can,
    toast,
    dismiss,
    openInbox,
    closeInbox,
    openPalette,
    closePalette,
    openSettings,
    sheet,
    openSheet,
    closeSheet,
    /** The resource's kind has a sheet. */
    hasSheet: (id: string) => hasSheet(live.graph.value, id),
    reconnect,
    tileSettings,
    openTileSettings,
    canRun,
    act,
    confirmed,
    cancelConfirm,
    /** Plan and run any action (not tied to an incident). */
    runAction: actions.run,
    /** Stop a running, cancellable task. */
    cancelTask: actions.cancelTask,
    /** Tasks with a cancel request in flight. */
    cancelling: actions.cancelling,
    /** Tasks from the `tasks` topic, plus the ones this tab started. */
    tasks: actions.tasks,
    running: actions.running,
    taskFor: actions.taskFor,
    /** This tab started the task (its end is toasted). */
    startedHere: actions.startedHere,
    /** Close the log dialog. */
    closeLog: () => {
      state.taskLog = undefined;
    },
    /** Show a cut-off note whole (NoteDialog). */
    showNote: (note: NoteView) => {
      state.paletteOpen = false;
      state.note = note;
    },
    closeNote: () => {
      state.note = undefined;
    },
    /** Open Home Assistant's panel on one of its lists (the tile's facets). */
    openHomePanel: (app: string, tab: string) => {
      state.paletteOpen = false;
      state.homePanel = { app, tab };
    },
    closeHomePanel: () => {
      state.homePanel = undefined;
    },
    stepUp: actions.stepUp,
    stepUpDone: actions.stepUpDone,
    withStepUp: actions.withStepUp,
    answer,
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
/** The page's UI, or undefined outside the app (a tile in a preview or a test). */
export const useUiIfAny = (): Ui | undefined => inject(KEY, undefined);
