/**
 * ⌘K actions: what the command bar offers to run on a resource (the Run
 * group), from open incidents and from the hub's listing
 * (`GET /api/actions?target=`), filtered by role, and run through the
 * normal safety ladder.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { nextTick } from 'vue';
import CommandPalette from '../src/components/CommandPalette.vue';
import { syncRoute } from '../src/lib/router';
import { actionCommands, searchCommands, type ActionOffer } from '../src/model/commands';
import { permissions } from '../src/model/roles';
import { resetIntegrations } from '../src/stores/integrations';
import { axeViolations } from './axe';
import { engine } from './fake-engine';
import { fakeFetch } from './fake-fetch';
import { docs, withIncidents } from './fixtures';
import { harness } from './harness';

const NAS = 'pve:stork/lxc/220'; // files; host-memory-pressure suggests Restart (T2)
const OFFERS: ActionOffer[] = [
  { id: 'pve.guest.backup', label: 'Back up files', tier: 'T1', available: true },
  { id: 'pve.guest.restart', label: 'Restart files', tier: 'T2', available: true },
  { id: 'pve.guest.snapshot.create', label: 'Snapshot files', tier: 'T1', available: false, unavailable: 'Add a control token to let Cha Dash snapshot guests on stork.' },
];

beforeEach(() => {
  history.replaceState(null, '', '/');
  syncRoute();
  resetIntegrations();
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

describe('actionCommands', () => {
  const d = docs(withIncidents);
  const base = (over: Partial<Parameters<typeof actionCommands>[0]> = {}) =>
    actionCommands({ graph: d.graph, inbox: d.inbox, act: vi.fn(), run: vi.fn(), refuse: vi.fn(), ...over });

  it('lists open incidents’ actions the role may run, never links', () => {
    const all = base();
    expect(all.find((c) => c.id === `act:pve.guest.restart@${NAS}`)).toMatchObject({ group: 'Run', label: 'Restart files', hint: 'T2' });
    expect(all.some((c) => c.id.startsWith('act:link.open'))).toBe(false);
    const viewer = base({ can: permissions('viewer') });
    expect(viewer.some((c) => c.hint === 'T1' || c.hint === 'T2')).toBe(false);
    expect(viewer.some((c) => c.hint === 'T0')).toBe(true);
    // Typing finds one by its resource's name.
    expect(searchCommands(all, 'restart files')[0]?.items[0]?.label).toBe('Restart files');
  });

  it('adds the hub’s offers for the subject once each, the unavailable ones disabled with the hub’s sentence', () => {
    const refuse = vi.fn();
    const run = vi.fn();
    const act = vi.fn();
    const list = base({ subject: NAS, offers: OFFERS, onlySubject: true, refuse, run, act });
    expect(list.map((c) => c.label)).toEqual(['Restart files', 'Back up files', 'Snapshot files']);
    const snap = list.find((c) => c.label === 'Snapshot files')!;
    expect(snap).toMatchObject({ hint: 'unavailable', disabled: 'Add a control token to let Cha Dash snapshot guests on stork.' });
    snap.run();
    expect(refuse).toHaveBeenCalledWith('Add a control token to let Cha Dash snapshot guests on stork.');
    list.find((c) => c.label === 'Back up files')!.run();
    expect(run).toHaveBeenCalledWith(OFFERS[0], NAS);
    // The incident's own action runs from the incident, so its progress shows there.
    list.find((c) => c.label === 'Restart files')!.run();
    expect(act.mock.calls[0]![0].id).toBe('host-memory-pressure');
  });
});

describe('the command bar’s Run group', () => {
  async function open(opts: { role?: string; focus?: string } = {}) {
    const e = engine({ offers: { [NAS]: OFFERS } });
    const hub = fakeFetch({ ...e.routes, 'GET /api/integrations/instances': { status: 200, body: [] } });
    const h = harness(docs(withIncidents));
    if (opts.role) h.session.state.user = { id: 'u2', username: 'kim', role: opts.role };
    if (opts.focus) {
      const cell = document.createElement('div');
      cell.dataset['item'] = `host@${opts.focus}`;
      const btn = document.createElement('button');
      btn.textContent = 'tile';
      cell.append(btn);
      document.body.append(cell);
      btn.focus();
    }
    h.ui.openPalette();
    h.mountIn(CommandPalette);
    await flushPromises();
    const input = document.body.querySelector<HTMLInputElement>('.palette input')!;
    return { ...h, e, hub, input };
  }
  const type = async (input: HTMLInputElement, text: string) => {
    input.value = text;
    input.dispatchEvent(new Event('input'));
    await nextTick();
  };
  const options = () => [...document.body.querySelectorAll<HTMLElement>('[role="option"]')];
  const runGroup = () => [...document.body.querySelectorAll<HTMLElement>('[role="group"]')].find((g) => g.querySelector('.grp')?.textContent?.startsWith('Run'));

  it('lists what the hub would plan on the searched resource, and runs one through the ladder', async () => {
    const { e, input, ui } = await open();
    await type(input, 'files');
    await vi.waitFor(() => expect(e.listed).toEqual([NAS]));
    await flushPromises();
    const run = runGroup()!;
    expect([...run.querySelectorAll('.ol')].map((o) => o.firstChild?.textContent)).toEqual(expect.arrayContaining(['Back up files', 'Restart files', 'Snapshot files']));
    const snap = options().find((o) => o.textContent?.includes('Snapshot files'))!;
    expect(snap.getAttribute('aria-disabled')).toBe('true');
    expect(snap.querySelector('.sr')?.textContent).toContain('Unavailable: Add a control token');
    expect(await axeViolations()).toEqual([]);

    options().find((o) => o.textContent?.includes('Back up files'))!.click();
    expect(ui.state.paletteOpen).toBe(false);
    await flushPromises();
    expect(e.plans).toEqual([{ action: 'pve.guest.backup', target: NAS }]);
  });

  it('says why an unavailable one can’t run, and plans nothing', async () => {
    const { e, input, ui } = await open();
    await type(input, 'files');
    await vi.waitFor(() => expect(options().some((o) => o.textContent?.includes('Snapshot files'))).toBe(true));
    options().find((o) => o.textContent?.includes('Snapshot files'))!.click();
    expect(ui.state.toasts.at(-1)).toMatchObject({ text: 'Add a control token to let Cha Dash snapshot guests on stork.', tone: 'error' });
    expect(e.plans).toEqual([]);
  });

  it('offers the focused tile’s actions before anything is typed, and announces them', async () => {
    const { e } = await open({ focus: NAS });
    await vi.waitFor(() => expect(e.listed).toEqual([NAS]));
    await flushPromises();
    expect(runGroup()?.querySelector('.grp')?.textContent).toBe('Run on files');
    expect(document.querySelector('.palette [role="status"]')?.textContent).toBe('3 actions for files.');
    expect(await axeViolations()).toEqual([]);
  });

  it('below admin, never asks for the integrations, and leaves out what the role can’t run', async () => {
    const { hub, input, e } = await open({ role: 'viewer' });
    expect(hub.calls.some((c) => c.path.startsWith('/api/integrations'))).toBe(false);
    // By ID: the name "files" also matches "View the failed task's log" letter by letter, a T0 a viewer may run.
    await type(input, NAS);
    await vi.waitFor(() => expect(e.listed).toEqual([NAS]));
    await flushPromises();
    // The hub filters its offers by policy; the page also drops tiers the role can't run.
    expect(runGroup()).toBeUndefined();
  });
});
