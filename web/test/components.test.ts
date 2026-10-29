import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { nextTick } from 'vue';
import Briefing from '../src/components/Briefing.vue';
import NeedsAttention from '../src/components/NeedsAttention.vue';
import HubBanner from '../src/components/HubBanner.vue';
import StaticBoard from '../src/board/StaticBoard.vue';
import BoardSurface from '../src/board/BoardSurface.vue';
import { defaultBoard } from '../src/model/defaultLayout';
import { setDisplayTimeZone } from '../src/lib/format';
import { applyTheme } from '../src/lib/theme';
import { toPointer } from '../src/lib/jsonpatch';
import { docs, healthy, resolvedIncident, withIncidents } from './fixtures';
import { engine } from './fake-engine';
import { fakeFetch } from './fake-fetch';
import { harness } from './harness';

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'] });
  vi.setSystemTime(new Date('2026-10-03T14:05:10Z'));
  setDisplayTimeZone('UTC');
  applyTheme('instrument-dark');
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

const board = (s = withIncidents) => defaultBoard(docs(s).graph, Date.parse(s.now));

describe('front page with today’s incidents', () => {
  it('briefs the problems first, as phrases that open the Inbox', async () => {
    const { mountIn, ui } = harness(docs(withIncidents));
    const w = mountIn(Briefing);
    const h1 = w.find('h1.sentence');
    expect(h1.text()).toContain('9 things need you');
    // The three worst by name, then the rest as one phrase that opens the Inbox.
    const phrases = h1.findAll('.phrase[role="button"]');
    expect(phrases.map((p) => p.text())).toEqual([
      'Nextcloud is stuck in maintenance mode',
      'sandbox is in internal‑error',
      "Backups on wren couldn't reach pbs‑magpie",
      '6 more',
    ]);
    expect(w.find('.sentence-2').text()).toContain('3 integrations to reconnect');
    expect(w.find('.clock').text()).toBe('14:05');
    await phrases[2]!.trigger('keydown', { key: 'Enter' });
    expect(ui.state.inboxOpen).toBe(true);
    expect(ui.state.inboxFocus).toBe('proxmox-wren.backup.daily');
  });

  it('switches to the headline with a deck in Paper', () => {
    applyTheme('paper');
    const { mountIn } = harness(docs(withIncidents));
    const w = mountIn(Briefing);
    // A headline names two and counts the rest.
    expect(w.find('h1.sentence').text()).toBe('Nine things need you: Nextcloud is stuck in maintenance mode, sandbox is in internal‑error and seven more.');
    expect(w.find('.mast-meta [aria-label="Search and commands"]').exists()).toBe(true);
    expect(w.find('.mast-meta .settings-menu [aria-label="Settings"]').exists()).toBe(true); // themes stay reachable in Paper
    expect(w.find('.sentence-2').text()).toContain('Next: Starling Point at 04:00. Three integrations need reconnecting in the Inbox.');
  });

  it('keeps Theme, Settings and Sign out behind the settings button', async () => {
    const { mountIn } = harness(docs(withIncidents));
    const w = mountIn(Briefing);
    const trigger = w.find('.settings-menu > button[aria-label="Settings"]');
    await trigger.trigger('click');
    await flushPromises();
    const items = () => w.findAll('[role="menuitem"]');
    expect(items().map((i) => i.text())).toEqual(['ThemeInstrument Dark', 'Settings', 'Sign out']);
    expect(document.activeElement).toBe(items()[0]!.element);
    // Arrows move through the menu and wrap.
    await w.find('[role="menu"]').trigger('keydown', { key: 'ArrowUp' });
    expect(document.activeElement).toBe(items()[2]!.element);

    // Theme opens the grid in the same popover, focused on the current theme.
    await items()[0]!.trigger('click');
    await flushPromises();
    expect(w.find('[role="menu"]').exists()).toBe(false);
    const current = w.find('[role="radio"][aria-checked="true"]');
    expect(current.text()).toContain('Instrument Dark');
    expect(document.activeElement).toBe(current.element);
    await current.trigger('keydown', { key: 'ArrowRight' });
    expect(w.find('[role="radio"][aria-checked="true"]').text()).toContain('Instrument Light');

    // Escape steps back to the menu first, then closes it.
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    await flushPromises();
    expect(items()[0]!.text()).toBe('ThemeInstrument Light');
    expect(document.activeElement).toBe(items()[0]!.element);
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    await flushPromises();
    expect(w.find('.settings-pop').exists()).toBe(false);
    expect(document.activeElement).toBe(trigger.element);

    await trigger.trigger('click');
    await flushPromises();
    await items()[2]!.trigger('click');
    expect(w.findComponent(Briefing).emitted('signOut')).toHaveLength(1);
    expect(w.find('.settings-pop').exists()).toBe(false);
  });

  it('mirrors problems into Needs attention with the suggested action and its tier', async () => {
    const { mountIn } = harness(docs(withIncidents));
    const w = mountIn(NeedsAttention);
    // The three worst show; the rest wait behind "Show all".
    expect(w.findAll('.strip')).toHaveLength(3);
    expect(w.find('.sh').text()).toContain('Needs attention · 9');
    await w.find('.attn-more').trigger('click');
    const strips = w.findAll('.strip');
    expect(strips).toHaveLength(9);
    expect(w.find('.attn-more').text()).toBe('Show fewer');
    const strip = (title: string) => strips.find((s) => s.text().includes(title))!;
    // Down first, oldest first: Nextcloud in maintenance mode, whose fix is T1; then the guest in internal-error, whose action opens Proxmox.
    expect(strips[0]!.text()).toContain('Nextcloud is stuck in maintenance mode');
    expect(strips[0]!.findAll('.acts button')[1]!.text()).toBe('Turn off maintenance mode T1');
    expect(strips[1]!.text()).toContain('sandbox is in internal-error');
    expect(strips[1]!.find('.k').text()).toBe('VM · sandbox · since 11:20');
    expect(strips[1]!.findAll('.acts button')[1]!.text()).toBe('Open sandbox in Proxmox T0');
    const pbs = strip('PBS datastore is filling up');
    expect(pbs.find('.k').text()).toBe('Backups · since 02:44');
    expect(pbs.find('[role="meter"]').attributes('aria-valuetext')).toBe('92%, threshold 90%');
    const act = pbs.findAll('.acts button')[1]!;
    expect(act.text()).toBe('Prune per policy T2');
    expect(strip('stork is running out of memory').find('.spark').exists()).toBe(true);
    // A backup v0.1 can only report: its action opens the backup jobs.
    expect(strip("Backups on wren couldn't reach pbs-magpie").findAll('.acts button')[1]!.text()).toBe('Open the backup jobs in Proxmox T0');
  });

  it('runs T1 straight away and asks to hold for T2, both through the engine', async () => {
    const { mountIn, ui, live } = harness(docs(withIncidents), { demo: true });
    const e = engine();
    fakeFetch(e.routes);
    const w = mountIn(NeedsAttention);
    await w.find('.attn-more').trigger('click');
    const strip = (title: string) => w.findAll('.strip').find((s) => s.text().includes(title))!;
    await strip('PBS datastore is filling up').findAll('.acts button')[1]!.trigger('click');
    await flushPromises();
    expect(ui.state.confirm?.action.id).toBe('pbs.datastore.prune');
    expect(e.approvals).toHaveLength(0);
    ui.cancelConfirm();
    await strip('Backup of homeassistant failed').findAll('.acts button')[1]!.trigger('click');
    await flushPromises();
    expect(e.approvals).toHaveLength(1);
    expect(live.fixes['backup-failed']?.state).toBe('accepted');
    expect(ui.state.toasts[0]?.text).toBe('Started: Back up homeassistant now.');
    expect(strip('Backup of homeassistant failed').find('.working').text()).toContain('Working · Ask the hub to back up homeassistant now · 10%');
  });

  it('renders the board with live state on the tiles', () => {
    const { mountIn } = harness(docs(withIncidents));
    const w = mountIn(BoardSurface, { board: board(), breakpoint: 'desktop' });
    expect(w.findAll('.sec').map((s) => s.find('.sh > span').text())).toEqual(['Home', 'Infrastructure', 'Media', 'Activity']);
    expect(w.findAll('.tile')).toHaveLength(17);
    const fleet = w.find('.tile[aria-label^="Fleet"]');
    expect(fleet.classes()).toContain('is-attn');
    expect(fleet.find('.fr .h [aria-label="memory 95% · no swap"]').exists()).toBe(true);
    const plex = w.find('.tile[aria-label^="Now playing"]');
    expect(plex.classes()).toContain('is-stale');
    expect(plex.find('.asof').text()).toBe('as of 13:26');
    expect(plex.text()).toContain('Plex rejected its token');
    const backups = w.find('.tile[aria-label^="Backups"]');
    expect(backups.find('.v').text()).toBe('92%');
    expect(w.find('.tile[aria-label^="Uptime Kuma"]').text()).toContain('12/14 up');
    // Incidents is wide beside the weather and the clock: three rows and the rest in the Inbox.
    const incidents = w.find('.tile[aria-label^="Incidents"]');
    expect(incidents.findAll('button.li')).toHaveLength(3);
    expect(incidents.text()).toContain('12 open'); // 9 problems and 3 repairs; the 4 hints stay in the Inbox
    expect(incidents.text()).toContain('+9 more in the Inbox');
    expect(w.find('.tile[aria-label^="Greenwich"]').text()).toContain('Weather data by Open-Meteo.com'); // named after the place
    expect(w.find('.tile[aria-label^="Clock"]').exists()).toBe(true);
  });

  it('places sections by the derived layout for each breakpoint', () => {
    const { mountIn } = harness(docs(withIncidents));
    const desk = mountIn(StaticBoard, { board: board(), breakpoint: 'desktop' });
    const secs = desk.findAll('.sec');
    expect((secs[1]!.element as HTMLElement).style.gridColumn).toBe('5 / span 8');
    expect((desk.find('.board').element as HTMLElement).style.getPropertyValue('--cols')).toBe('12');
    const phone = mountIn(StaticBoard, { board: board(), breakpoint: 'phone' });
    expect((phone.find('.board').element as HTMLElement).style.getPropertyValue('--cols')).toBe('4');
    expect(phone.findAll('.sec').every((s) => (s.element as HTMLElement).style.gridColumn === '1 / span 4')).toBe(true);
  });

  it('collapses Needs attention live when the last problem resolves', async () => {
    const d = docs(withIncidents);
    const { mountIn, ws } = harness(d);
    const w = mountIn(NeedsAttention);
    const ids = ['nextcloud-maintenance', 'backup-failed', 'pbs-datastore-filling', 'speedtest-slow', 'host-memory-pressure', 'qbittorrent-oom', 'proxmox-aviary.guest.221'];
    // The two unreachable backups clear with the next night's runs, after the timeline ends.
    const overnight = ['proxmox-wren.backup.daily', 'proxmox-owl.backup.daily'];
    const patch = [
      ...ids.map((id) => ({ op: 'replace', path: toPointer('inbox', 'incidents', id), value: resolvedIncident(id) })),
      ...overnight.map((id) => ({
        op: 'replace',
        path: toPointer('inbox', 'incidents', id),
        value: { ...d.inbox.incidents[id]!, status: 'resolved', phase: 'resolved', resolved_at: '2026-10-04T02:30:00Z', resolved_by: 'auto' },
      })),
    ];
    ws.push({ op: 'diff', id: ws.subId('inbox'), seq: 3000, patch });
    await nextTick();
    vi.advanceTimersByTime(1000);
    await nextTick();
    expect(w.find('.attn-sec').exists()).toBe(false);
  });
});

describe('a quiet home', () => {
  it('says all quiet and shows empty states, not zeros', () => {
    const { mountIn } = harness(docs(healthy));
    const b = mountIn(Briefing);
    expect(b.find('h1.sentence').text()).toMatch(/^All quiet · 26 services healthy · backups done \d\d:\d\d · array \d+%/);
    const n = mountIn(NeedsAttention);
    expect(n.find('.attn-sec').exists()).toBe(false);
    const w = mountIn(BoardSurface, { board: board(healthy), breakpoint: 'desktop' });
    expect(w.findAll('.tile.is-attn, .tile.is-down')).toHaveLength(0);
    // Only the standing hints are open, and hints live in the Inbox, not on the tile.
    const incidents = w.find('.tile[aria-label^="Incidents"]');
    expect(incidents.findAll('button.li')).toHaveLength(0);
    expect(incidents.text()).toContain('Nothing needs you');
    expect(w.find('.tile[aria-label^="Now playing"]').text()).toContain('Nothing playing');
    document.body.innerHTML = '';
    const none = docs(healthy);
    none.inbox.incidents = {};
    const empty = harness(none).mountIn(BoardSurface, { board: board(healthy), breakpoint: 'desktop' });
    expect(empty.find('.tile[aria-label^="Incidents"]').text()).toContain('Nothing needs you');
  });
});

describe('empty and offline', () => {
  it('shows helpful empty states when the graph has nothing', () => {
    const { mountIn } = harness({ graph: { resources: {}, relations: {} }, metrics: { series: {} }, inbox: { now: withIncidents.now, incidents: {} } });
    const w = mountIn(BoardSurface, { board: defaultBoard(undefined), breakpoint: 'desktop' });
    expect(w.find('.tile[aria-label^="Cameras"]').text()).toContain('No cameras');
    expect(w.find('.tile[aria-label^="Fleet"]').text()).toContain('No hosts yet');
    expect(w.find('.tile[aria-label^="Backups"]').text()).toContain('No backup server');
    expect(w.find('.tile[aria-label^="Containers"]').text()).toContain('No Docker engines');
    expect(w.text()).not.toMatch(/\b0%/);
  });

  it('says it is the Demo Home in demo mode, until a connection banner takes over', async () => {
    const { mountIn, ws, live } = harness(docs(withIncidents), { demo: true });
    const brief = mountIn(Briefing);
    const onSignOut = vi.fn();
    const banner = mountIn(HubBanner, { onSignOut });
    expect(brief.find('.wordmark .demo-tag').text()).toBe('Demo');
    expect(brief.find('.brief-side .sr').text()).toContain('Simulated time');
    expect(banner.find('.banner.demo').text()).toContain("You're looking at the Demo Home");
    await banner.find('.banner.demo button').trigger('click');
    expect(onSignOut).toHaveBeenCalledOnce();
    ws.drop();
    vi.advanceTimersByTime(4000);
    live.tick();
    await nextTick();
    expect(banner.find('.banner.demo').exists()).toBe(false);
    expect(banner.find('.banner').text()).toContain('Cha Dash hub unreachable');
    // The wordmark keeps saying so.
    expect(brief.find('.wordmark .demo-tag').exists()).toBe(true);
  });

  it('shows no demo marks outside demo mode', () => {
    const { mountIn } = harness(docs(withIncidents));
    expect(mountIn(Briefing).find('.demo-tag').exists()).toBe(false);
    expect(mountIn(HubBanner).find('.banner').exists()).toBe(false);
  });

  it('keeps last-known values hatched under the offline banner', async () => {
    const { mountIn, ws, live } = harness(docs(withIncidents));
    const banner = mountIn(HubBanner);
    const w = mountIn(BoardSurface, { board: board(), breakpoint: 'desktop' });
    expect(banner.find('.banner').exists()).toBe(false);
    ws.drop();
    vi.advanceTimersByTime(4000);
    live.tick();
    await nextTick();
    expect(banner.find('.banner').text()).toContain('Cha Dash hub unreachable since 14:05');
    // Every tile is hatched but the clock, which keeps the time on its own.
    const clock = (t: { attributes: (k: string) => string | undefined }) => (t.attributes('aria-label') ?? '').startsWith('Clock');
    expect(w.findAll('.tile').filter((t) => !clock(t)).every((t) => t.classes().includes('is-stale'))).toBe(true);
    expect(w.findAll('.tile').filter(clock).map((t) => t.classes().includes('is-stale'))).toEqual([false]);
    expect(w.find('.tile[aria-label^="Backups"] .v').text()).toBe('92%');
  });
});
