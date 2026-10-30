/**
 * Regression tests for the v0.2 review's accessibility and web-hardening
 * fixes: Esc under the step-up, hold-to-confirm for assistive clicks, one
 * live region for task progress, readable blast lists, focus in approvals,
 * the Settings Tab trap, motion tokens, the action dialogs' lazy CSS,
 * http(s)-only links, and the service worker's same-origin clicks.
 */
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils';
import { nextTick } from 'vue';
import ApprovalItem from '../src/components/ApprovalItem.vue';
import HoldToConfirm from '../src/components/HoldToConfirm.vue';
import InboxSheet from '../src/components/InboxSheet.vue';
import StepUpDialog from '../src/components/StepUpDialog.vue';
import TaskPresence from '../src/components/TaskPresence.vue';
import Tile from '../src/components/Tile.vue';
import SettingsSheet from '../src/settings/SettingsSheet.vue';
import { setDisplayTimeZone } from '../src/lib/format';
import { toPointer } from '../src/lib/jsonpatch';
import { navigate, view } from '../src/lib/router';
import { resourceLinks, safeUrl, type Incident } from '../src/model/types';
import swSource from '../sw/service-worker.js?raw';
import approvalSource from '../src/components/ApprovalItem.vue?raw';
import { axeViolations } from './axe';
import { planFor, taskFor } from './fake-engine';
import { fakeFetch } from './fake-fetch';
import { docs, withIncidents } from './fixtures';
import { harness } from './harness';

enableAutoUnmount(afterEach);

// Read from disk: Vitest doesn't process CSS, so a ?raw import of a .css file is empty.
const SRC = join(import.meta.dirname, '..', 'src');
const walk = (dir: string): string[] => readdirSync(dir, { withFileTypes: true }).flatMap((e) => (e.isDirectory() ? walk(join(dir, e.name)) : [join(dir, e.name)]));
const chromeCss = readFileSync(join(SRC, 'styles', 'chrome.css'), 'utf8');
const actCss = readFileSync(join(SRC, 'styles', 'act-dialog.css'), 'utf8');

beforeEach(() => {
  setDisplayTimeZone('UTC');
  navigate('/', { replace: true });
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

async function settle(): Promise<void> {
  await vi.dynamicImportSettled();
  await flushPromises();
  await nextTick();
}

async function until(cond: () => boolean, what: string): Promise<void> {
  for (let i = 0; i < 400 && !cond(); i++) {
    await settle();
    await new Promise((r) => setTimeout(r, 5));
  }
  if (!cond()) throw new Error(`timed out waiting for ${what}`);
}

const esc = (target: EventTarget = document) => target.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));

const APPROVAL: Incident = {
  id: 'approval.apv_1',
  scenario: 'approval',
  class: 'approval',
  severity: 'attention',
  status: 'open',
  phase: 'awaiting_approval',
  title: 'backup script (owner) asks to run: Restart sonarr',
  summary: 'Planned through the API by a token, a T2 action.',
  target: 'docker:magpie/c/sonarr',
  opened_at: '2026-10-03T14:00:00Z',
  causes: [],
  blast_radius: [{ resource: 'app:sonarr', via: 'runs_on', depth: 1 }],
  approval: {
    id: 'apv_1',
    state: 'awaiting_approval',
    plan: { id: 'p_1', hash: 'sha256:abc', action: 'docker.container.restart', label: 'Restart sonarr', target: 'docker:magpie/c/sonarr', tier: 'T2', steps: [{ label: 'Ask the node to restart sonarr' }], impact: [{ resource: 'app:sonarr', via: 'runs_on', depth: 1 }] },
    originator: { kind: 'token', id: 'tok_a', name: 'backup script (owner)', user: 'u1', user_name: 'owner' },
    requested_at: '2026-10-03T14:00:00Z',
    expires_at: '2026-10-03T15:00:00Z',
  },
};

function withApproval() {
  const d = docs(withIncidents);
  d.inbox.incidents[APPROVAL.id] = structuredClone(APPROVAL);
  return d;
}

const PASSWORD_BEGIN = { 'POST /api/auth/step-up/begin': { status: 200, body: { method: 'password' } } };

describe('Esc on the step-up closes only the step-up', () => {
  it('leaves Settings open, whether the dialog stops it or the sheet checks', async () => {
    fakeFetch({ ...PASSWORD_BEGIN, 'GET /api/integrations/catalog': { status: 200, body: [] }, 'GET /api/integrations/instances': { status: 200, body: [] } });
    const h = harness(docs(withIncidents));
    navigate('/settings/integrations', { replace: true });
    h.mountIn(SettingsSheet);
    let answer: boolean | undefined;
    h.ui.state.stepUp = { reason: 'Change the security level', fresh: false, resolve: (ok: boolean) => (answer = ok) };
    h.mountIn(StepUpDialog);
    await until(() => !!document.querySelector('dialog.stepup input[type="password"]'), 'the password field');

    // From inside the dialog: it doesn't bubble to the sheet.
    esc(document.querySelector('dialog.stepup input')!);
    await settle();
    expect(view.value.name).toBe('settings');
    // Even if it reached the document, the sheet sees the step-up and stays.
    esc();
    await settle();
    expect(view.value.name).toBe('settings');
    expect(answer).toBeUndefined();

    // With nothing above it, Esc closes Settings as before.
    h.ui.stepUpDone(false);
    await settle();
    document.querySelectorAll('dialog[open]').forEach((d) => d.removeAttribute('open'));
    esc();
    await until(() => view.value.name !== 'settings', 'Settings to close');
  });

  it('leaves the Inbox open during an approval’s step-up', async () => {
    fakeFetch(PASSWORD_BEGIN);
    const h = harness(withApproval());
    h.mountIn(InboxSheet);
    h.ui.openInbox();
    await until(() => !!document.querySelector('.apv .hold'), 'the approval');
    // Approve with the assistive two-step (a click with no press before it).
    const hold = document.querySelector<HTMLButtonElement>('.apv .hold')!;
    hold.click();
    hold.click();
    await until(() => !!h.ui.state.stepUp, 'the step-up request');
    h.mountIn(StepUpDialog);
    await until(() => !!document.querySelector('dialog.stepup input[type="password"]'), 'the password field');
    esc();
    await settle();
    expect(h.ui.state.inboxOpen).toBe(true);
    h.ui.stepUpDone(false);
    await settle();
  });
});

describe('axe over the step-up’s failure states', () => {
  it('the hub couldn’t start it: the error and Try again', async () => {
    fakeFetch({ 'POST /api/auth/step-up/begin': { status: 500, body: { error: { code: 'internal', message: 'The hub couldn’t start that. Try again.' } } } });
    const h = harness(docs(withIncidents));
    h.ui.state.stepUp = { reason: 'Delete snapshot pre-upgrade', fresh: false, resolve: () => undefined };
    h.mountIn(StepUpDialog);
    await until(() => [...document.querySelectorAll('dialog.stepup button')].some((b) => b.textContent?.trim() === 'Try again'), 'Try again');
    expect(document.querySelector('dialog.stepup .err[role="alert"]')?.textContent).toContain('Try again');
    expect(document.activeElement?.textContent?.trim()).toBe('Try again');
    expect(await axeViolations()).toEqual([]);
  });

  it('a wrong password, said at the field', async () => {
    fakeFetch({ ...PASSWORD_BEGIN, 'POST /api/auth/step-up': { status: 401, body: { error: { code: 'bad_password', message: 'That password isn’t right.' } } } });
    const h = harness(docs(withIncidents));
    h.ui.state.stepUp = { reason: 'Delete snapshot pre-upgrade', fresh: false, resolve: () => undefined };
    h.mountIn(StepUpDialog);
    await until(() => !!document.querySelector('dialog.stepup input[type="password"]'), 'the password field');
    const pw = document.querySelector<HTMLInputElement>('dialog.stepup input[type="password"]')!;
    pw.value = 'nope';
    pw.dispatchEvent(new Event('input'));
    document.querySelector<HTMLFormElement>('dialog.stepup form')!.dispatchEvent(new Event('submit', { cancelable: true }));
    await until(() => pw.getAttribute('aria-invalid') === 'true', 'the password error');
    expect(document.querySelector('dialog.stepup .help.bad')?.textContent).toBe('That password isn’t right.');
    expect(await axeViolations()).toEqual([]);
  });
});

describe('hold-to-confirm without a hold', () => {
  it('a click with no press before it arms a two-step Confirm, said in a live hint', async () => {
    const w = mount(HoldToConfirm, { props: { label: 'restart' }, attachTo: document.body });
    const btn = w.find('button.hold');
    const hint = w.find('[aria-live="polite"]');
    expect(btn.attributes('aria-label')).toBe('Hold to restart');
    expect(hint.text()).toBe('');

    (btn.element as HTMLButtonElement).click();
    await nextTick();
    expect(btn.attributes('aria-label')).toBe('Confirm: restart');
    expect(btn.text()).toContain('Confirm: restart');
    expect(hint.text()).toContain('Press again to restart');
    expect(w.emitted('confirm')).toBeUndefined();

    (btn.element as HTMLButtonElement).click();
    await new Promise((r) => setTimeout(r, 500));
    expect(w.emitted('confirm')).toHaveLength(1);
  });

  it('a pointer tap is still a (too short) hold: nothing arms, nothing confirms', async () => {
    const w = mount(HoldToConfirm, { props: { label: 'restart' }, attachTo: document.body });
    const btn = w.find('button.hold');
    await btn.trigger('pointerdown');
    await btn.trigger('pointerup');
    await btn.trigger('click');
    await new Promise((r) => setTimeout(r, 20));
    expect(btn.attributes('aria-label')).toBe('Hold to restart');
    expect(w.emitted('confirm')).toBeUndefined();
  });

  it('a held key does not arm the two-step either', async () => {
    const w = mount(HoldToConfirm, { props: { label: 'restart' }, attachTo: document.body });
    const btn = w.find('button.hold');
    await btn.trigger('keydown', { key: ' ' });
    await btn.trigger('keyup', { key: ' ' });
    await btn.trigger('click');
    expect(btn.attributes('aria-label')).toBe('Hold to restart');
  });
});

describe('task progress is announced once', () => {
  const p = planFor('pve.guest.restart', 'pve:stork/lxc/220');
  const t1 = () => taskFor(p, { id: 't1', progress: 0.1, step: 'Stop the container', cancellable: true });

  it('one live region, speaking step changes and the end, never the percentage', async () => {
    const h = harness({ ...docs(withIncidents), tasks: { tasks: { t1: t1() } } });
    h.mountIn(InboxSheet);
    h.mountIn(TaskPresence);
    h.ui.openInbox('host-memory-pressure');
    await settle();
    const announcer = document.querySelector('[data-task-announcer]')!;
    // Already running when the page opened: not news.
    expect(announcer.textContent).toBe('');
    // The visual presence and the incident aren't live regions.
    expect(document.querySelector('.task-presence')?.getAttribute('aria-live')).toBeNull();
    expect(document.querySelector('.task-presence')?.getAttribute('role')).toBeNull();
    expect(document.querySelector('.working')?.getAttribute('role')).toBeNull();
    expect(await axeViolations()).toEqual([]);

    // Progress alone says nothing.
    h.ws.push({ op: 'diff', id: h.ws.subId('tasks'), seq: 2, patch: [{ op: 'replace', path: toPointer('tasks', 'tasks', 't1', 'progress'), value: 0.6 }] });
    await settle();
    expect(announcer.textContent).toBe('');

    // A new step does, without a number.
    h.ws.push({ op: 'diff', id: h.ws.subId('tasks'), seq: 3, patch: [{ op: 'replace', path: toPointer('tasks', 'tasks', 't1', 'step'), value: 'Start the container' }] });
    await settle();
    expect(announcer.textContent).toBe('Restart files: Start the container.');
    expect(announcer.textContent).not.toMatch(/%/);

    // The end, for a task this tab didn't start (no toast says it).
    h.ws.push({ op: 'diff', id: h.ws.subId('tasks'), seq: 4, patch: [{ op: 'replace', path: toPointer('tasks', 'tasks', 't1', 'state'), value: 'succeeded' }] });
    await settle();
    expect(announcer.textContent).toBe('Done: Restart files.');
    expect(document.querySelectorAll('[aria-live]:not([aria-live="off"]) [data-task-announcer], [data-task-announcer] [aria-live]').length).toBe(0);
  });

  it('IncidentItem, working with progress and Cancel, passes axe', async () => {
    const h = harness({ ...docs(withIncidents), tasks: { tasks: {} } });
    h.mountIn(InboxSheet);
    h.ui.openInbox('host-memory-pressure');
    await settle();
    const inc = h.live.inbox.value!.incidents['host-memory-pressure']!;
    const a = inc.suggested!;
    const task = taskFor(planFor(a.id, a.target), { id: 't2', progress: 0.5, cancellable: true });
    h.ws.push({ op: 'diff', id: h.ws.subId('tasks'), seq: 2, patch: [{ op: 'add', path: toPointer('tasks', 'tasks', 't2'), value: task }] });
    await settle();
    expect(document.querySelector('.ib.open .working')?.textContent).toContain('50%');
    expect(document.querySelector('.ib.open .working-cancel')).not.toBeNull();
    expect(await axeViolations()).toEqual([]);
  });
});

describe('the blast list stays readable', () => {
  it('no opacity on resting rows; the lit row is marked with colour and a bar', () => {
    const rule = (sel: string) => {
      const m = chromeCss.match(new RegExp(`(^|\\n)${sel.replace(/[.]/g, '\\.')} \\{([^}]*)\\}`));
      return m?.[2] ?? '';
    };
    expect(rule('.blast li')).not.toMatch(/opacity/);
    expect(rule('.blast li')).toMatch(/color: var\(--text-2\)/);
    expect(rule('.blast li.on')).toMatch(/color: var\(--text\)/);
    expect(rule('.blast li.on')).not.toMatch(/opacity/);
  });

  it('the approval’s “What this affects” is never dimmed', async () => {
    fakeFetch({});
    const h = harness(withApproval());
    const w = h.mountIn(ApprovalItem, { incident: APPROVAL });
    await settle();
    expect(w.find('.apv-blast li').exists()).toBe(true);
    expect(approvalSource).toMatch(/\.apv-blast li \{\s*color: var\(--text\);\s*\}/);
    expect(approvalSource).not.toMatch(/opacity/);
  });
});

describe('focus in an approval', () => {
  it('Deny moves focus into the reason; Back returns it to Deny', async () => {
    fakeFetch({});
    const h = harness(withApproval());
    const w = h.mountIn(ApprovalItem, { incident: APPROVAL });
    await settle();
    await w.find('.apv-deny-btn').trigger('click');
    await settle();
    expect(document.activeElement).toBe(w.find('.apv-deny input').element);
    await w.findAll('button').find((b) => b.text() === 'Back')!.trigger('click');
    await settle();
    expect(document.activeElement).toBe(w.find('.apv-deny-btn').element);
  });

  it('after a try, focus lands on the rebuilt ring, not the page', async () => {
    fakeFetch({ 'POST /api/approvals/apv_1/approve': { status: 409, body: { error: { code: 'plan_changed', message: 'The target changed since this was planned.' } } } });
    const h = harness(withApproval());
    const w = h.mountIn(ApprovalItem, { incident: APPROVAL });
    await settle();
    (w.find('.hold').element as HTMLElement).focus();
    w.findComponent(HoldToConfirm).vm.$emit('confirm');
    await settle();
    h.ui.stepUpDone(true);
    await settle();
    expect(w.find('.err').text()).toBe('The target changed since this was planned.');
    expect(document.activeElement).toBe(w.find('.hold').element);
  });
});

describe('the Settings sheet keeps Tab inside', () => {
  it('Tab from the last control goes back to the first, not to the task strip', async () => {
    fakeFetch({ 'GET /api/integrations/catalog': { status: 200, body: [] }, 'GET /api/integrations/instances': { status: 200, body: [] } });
    const h = harness({ ...docs(withIncidents), tasks: { tasks: { t1: taskFor(planFor('pve.guest.restart', 'pve:stork/lxc/220'), { id: 't1', cancellable: true }) } } });
    navigate('/settings/integrations');
    h.mountIn(TaskPresence);
    h.mountIn(SettingsSheet);
    await settle();
    const sheet = document.querySelector<HTMLElement>('.settings')!;
    const items = [...sheet.querySelectorAll<HTMLElement>('button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])')];
    items.at(-1)!.focus();
    const e = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true });
    document.activeElement!.dispatchEvent(e);
    expect(e.defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(items[0]);
  });
});

describe('motion and CSS', () => {
  const sources = Object.fromEntries(walk(SRC).filter((f) => /\.(css|vue)$/.test(f)).map((f) => [f.slice(SRC.length + 1), readFileSync(f, 'utf8')]));

  it('no transition hard-codes a duration: the motion tokens decide', () => {
    const bad: string[] = [];
    for (const [file, text] of Object.entries(sources)) {
      for (const m of text.matchAll(/transition(?:-duration)?\s*:\s*([^;{}]*)/g)) {
        if (/(^|[\s,(])\d+(\.\d+)?m?s\b/.test(m[1]!.replace(/var\([^)]*\)/g, ''))) bad.push(`${file}: ${m[0]}`);
      }
    }
    expect(bad).toEqual([]);
  });

  it('the action dialogs’ CSS is no longer render-blocking', () => {
    expect(chromeCss).not.toMatch(/\.act-(dlg|impact|hub|note|lede)/);
    expect(actCss).toMatch(/\.act-impact li\.is-self/);
    // UiDialog's shell loads with the first dialog, not with the first paint.
    expect(readFileSync(join(SRC, 'ui', 'dialog.css'), 'utf8')).toMatch(/\.dlg \{/);
    for (const css of ['styles/base.css', 'styles/chrome.css', 'ui/ui.css']) expect(readFileSync(join(SRC, css), 'utf8')).not.toMatch(/\.dlg\b/);
  });
});

describe('links are http(s) only', () => {
  it('safeUrl and resourceLinks refuse javascript: and data:', () => {
    expect(safeUrl('javascript:alert(1)')).toBeUndefined();
    expect(safeUrl('JaVaScRiPt:alert(1)')).toBeUndefined();
    expect(safeUrl(' https://x.example')).toBeUndefined();
    expect(safeUrl('data:text/html,hi')).toBeUndefined();
    expect(safeUrl('https://plex.example:32400/web')).toBe('https://plex.example:32400/web');
    const r = { id: 'app:x', kind: 'app', links: [{ url: 'javascript:alert(1)' }, { url: 'http://x.example' }] } as Parameters<typeof resourceLinks>[0];
    expect(resourceLinks(r).map((l) => l.url)).toEqual(['http://x.example']);
  });

  it('a tile given a javascript: link renders no href', async () => {
    const h = harness(docs(withIncidents));
    const w = h.mountIn(Tile, { size: 'small', name: 'Evil', href: 'javascript:alert(1)' });
    await settle();
    expect(w.findAll('a').some((a) => (a.attributes('href') ?? '').toLowerCase().startsWith('javascript'))).toBe(false);
    const ok = h.mountIn(Tile, { size: 'small', name: 'Fine', href: 'https://fine.example/' });
    await settle();
    expect(ok.find('a').attributes('href')).toBe('https://fine.example/');
  });
});

describe('the service worker’s notification click', () => {
  function sw() {
    const listeners: Record<string, (e: unknown) => void> = {};
    const opened: string[] = [];
    const self = {
      __CHADASH_PRECACHE__: [],
      location: { origin: 'https://hub.test' },
      addEventListener: (type: string, fn: (e: unknown) => void) => (listeners[type] = fn),
      skipWaiting: async () => undefined,
      clients: { claim: async () => undefined, matchAll: async () => [], openWindow: async (u: string) => void opened.push(u) },
      registration: { showNotification: async () => undefined },
    };
    new Function('self', 'caches', 'fetch', 'Request', swSource)(self, {}, async () => new Response(''), class {});
    async function click(path: unknown): Promise<string | undefined> {
      let wait: Promise<unknown> = Promise.resolve();
      listeners['notificationclick']!({ notification: { close: () => undefined, data: { path } }, waitUntil: (p: Promise<unknown>) => (wait = p) });
      await wait;
      return opened.pop();
    }
    return { click };
  }

  it('stays on the hub’s origin, backslash tricks included', async () => {
    const { click } = sw();
    const BS = String.fromCharCode(92);
    expect(await click(`/${BS}evil.example`)).toBe('https://hub.test/');
    expect(await click(`/${BS}${BS}evil.example/x`)).toBe('https://hub.test/');
    expect(await click('//evil.example')).toBe('https://hub.test/');
    expect(await click('https://evil.example/')).toBe('https://hub.test/');
    expect(await click('/inbox/host-memory-pressure?x=1')).toBe('https://hub.test/inbox/host-memory-pressure?x=1');
  });
});
