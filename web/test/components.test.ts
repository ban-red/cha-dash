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
    expect(h1.text()).toContain('4 things need you');
    const phrases = h1.findAll('button.phrase');
    expect(phrases.map((p) => p.text())).toEqual([
      'Backup of homeassistant failed',
      'PBS datastore is filling up',
      'jupiter\u2011nas is running out of memory',
      'qBittorrent ran out of memory',
    ]);
    expect(w.find('.sentence-2').text()).toContain('2 integrations to reconnect');
    expect(w.find('.clock').text()).toBe('14:05');
    await phrases[1]!.trigger('click');
    expect(ui.state.inboxOpen).toBe(true);
    expect(ui.state.inboxFocus).toBe('pbs-datastore-filling');
  });

  it('switches to the headline with a deck in Paper', () => {
    applyTheme('paper');
    const { mountIn } = harness(docs(withIncidents));
    const w = mountIn(Briefing);
    expect(w.find('h1.sentence').text()).toMatch(/^Four things need you: backup of homeassistant failed, PBS datastore is filling up/);
    expect(w.find('h1.sentence').text()).toContain('filling up, jupiter‑nas is running out of memory and qBittorrent ran out of memory.');
    expect(w.find('.sentence-2').text()).toContain('Next: Black Clover at 04:00. Two integrations need reconnecting in the Inbox.');
  });

  it('mirrors problems into Needs attention with the suggested action and its tier', () => {
    const { mountIn } = harness(docs(withIncidents));
    const w = mountIn(NeedsAttention);
    const strips = w.findAll('.strip');
    expect(strips).toHaveLength(4);
    expect(w.find('.sh').text()).toContain('Needs attention · 4');
    const pbs = strips[1]!;
    expect(pbs.find('.k').text()).toBe('Backups · since 02:44');
    expect(pbs.find('[role="meter"]').attributes('aria-valuetext')).toBe('92%, threshold 90%');
    const act = pbs.findAll('.acts button')[1]!;
    expect(act.text()).toBe('Prune per policy T2');
    expect(strips[2]!.find('.spark').exists()).toBe(true); // jupiter-nas memory
  });

  it('runs T1 straight away and asks to hold for T2', async () => {
    const { mountIn, ui, live } = harness(docs(withIncidents), { demo: true });
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ id: 'backup-failed' }), { status: 202 }));
    vi.stubGlobal('fetch', fetchMock);
    const w = mountIn(NeedsAttention);
    const [backup, pbs] = w.findAll('.strip');
    await pbs!.findAll('.acts button')[1]!.trigger('click');
    expect(ui.state.confirm?.action.id).toBe('pbs.datastore.prune');
    expect(fetchMock).not.toHaveBeenCalled();
    await backup!.findAll('.acts button')[1]!.trigger('click');
    await flushPromises();
    expect(fetchMock.mock.calls[0]![0]).toBe('/api/demo/incidents/backup-failed/fix');
    expect(live.fixes['backup-failed']?.state).toBe('accepted');
    expect(ui.state.toasts[0]?.text).toBe('Started: Back up homeassistant now.');
    expect(w.findAll('.strip')[0]!.find('.working').text()).toContain('Working · failed');
  });

  it('renders the board with live state on the tiles', () => {
    const { mountIn } = harness(docs(withIncidents));
    const w = mountIn(BoardSurface, { board: board(), breakpoint: 'desktop' });
    expect(w.findAll('.sec').map((s) => s.find('.sh > span').text())).toEqual(['Home', 'Infrastructure', 'Media', 'Activity']);
    expect(w.findAll('.tile')).toHaveLength(15);
    const fleet = w.find('.tile[aria-label^="Fleet"]');
    expect(fleet.classes()).toContain('is-attn');
    expect(fleet.find('.fr .h [aria-label="memory 95% · no swap"]').exists()).toBe(true);
    const plex = w.find('.tile[aria-label^="Now playing"]');
    expect(plex.classes()).toContain('is-stale');
    expect(plex.find('.asof').text()).toBe('as of 13:26');
    expect(plex.text()).toContain('Plex rejected its token');
    const backups = w.find('.tile[aria-label^="Backups"]');
    expect(backups.find('.v').text()).toBe('92%');
    expect(w.find('.tile[aria-label^="Uptime Kuma"]').text()).toContain('8/9 up');
    expect(w.find('.tile[aria-label^="Incidents"]').findAll('button.li')).toHaveLength(6);
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
    const { mountIn, ws } = harness(docs(withIncidents));
    const w = mountIn(NeedsAttention);
    const ids = ['backup-failed', 'pbs-datastore-filling', 'host-memory-pressure', 'qbittorrent-oom'];
    const patch = ids.map((id) => {
      const inc = id === 'qbittorrent-oom' ? { ...resolvedIncident(id) } : resolvedIncident(id);
      return { op: 'replace', path: toPointer('inbox', 'incidents', id), value: inc };
    });
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
    expect(b.find('h1.sentence').text()).toMatch(/^All quiet · 18 services healthy · backups done \d\d:\d\d · array \d+%/);
    const n = mountIn(NeedsAttention);
    expect(n.find('.attn-sec').exists()).toBe(false);
    const w = mountIn(BoardSurface, { board: board(healthy), breakpoint: 'desktop' });
    expect(w.findAll('.tile.is-attn, .tile.is-down')).toHaveLength(0);
    expect(w.find('.tile[aria-label^="Incidents"]').text()).toContain('Nothing needs you');
    expect(w.find('.tile[aria-label^="Now playing"]').text()).toContain('Nothing playing');
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
    expect(w.findAll('.tile').every((t) => t.classes().includes('is-stale'))).toBe(true);
    expect(w.find('.tile[aria-label^="Backups"] .v').text()).toBe('92%');
  });
});
