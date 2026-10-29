import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, mount } from '@vue/test-utils';
import { nextTick } from 'vue';
import InboxSheet from '../src/components/InboxSheet.vue';
import HoldToConfirm from '../src/components/HoldToConfirm.vue';
import ConfirmDialog from '../src/components/ConfirmDialog.vue';
import { setDisplayTimeZone } from '../src/lib/format';
import { toPointer } from '../src/lib/jsonpatch';
import { docs, healthy, resolvedIncident, withIncidents } from './fixtures';
import { engine } from './fake-engine';
import { fakeFetch } from './fake-fetch';
import { harness } from './harness';

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

describe('Inbox sheet', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] });
    vi.setSystemTime(new Date('2026-10-03T14:05:10Z'));
    setDisplayTimeZone('UTC');
  });

  async function openOn(id: string, data = docs(withIncidents), demo = true) {
    const h = harness(data, { demo });
    h.mountIn(InboxSheet);
    h.ui.openInbox(id);
    await flushPromises();
    await nextTick();
    const panel = document.body.querySelector<HTMLElement>('.inbox')!;
    return { ...h, panel };
  }

  it('groups by status, and opens on the focused incident with facts, causes and blast radius', async () => {
    const { panel } = await openOn('pbs-datastore-filling');
    expect(panel).not.toBeNull();
    const groups = [...panel.querySelectorAll('.ib-grp')].map((g) => g.textContent?.trim());
    // qBittorrent is restarting by itself; the hints are standing advice, in their own group.
    expect(groups).toEqual(['Open · 11', 'Being fixed · 1', 'Security hints · 4']);
    const titles = [...panel.querySelectorAll('.ib-title')].map((t) => t.textContent);
    // problems first (by severity, then age), then repairs
    expect(titles.slice(0, 11)).toEqual([
      'Nextcloud is stuck in maintenance mode',
      'sandbox is in internal-error',
      "Backups on wren couldn't reach pbs-magpie",
      'Backup of homeassistant failed',
      'Backup of win-games failed',
      'PBS datastore is filling up',
      'Internet downloads are slow',
      'stork is running out of memory',
      "Karakeep's API changed",
      "Unraid's API changed",
      'Plex rejected its token',
    ]);
    expect(titles.slice(11)).toEqual([
      'qBittorrent ran out of memory',
      'AdGuard Home sends its credentials in clear text',
      "magpie's socket proxy is too broad",
      'Frigate grants admin without a sign-in',
      "Traefik's dashboard has no auth",
    ]);
    const open = panel.querySelector('.ib.open')!;
    expect(open.querySelector('.ib-title')!.textContent).toBe('PBS datastore is filling up');
    expect(open.textContent).toContain('What Cha Dash saw');
    expect(open.textContent).toContain('Used %92');
    expect(open.textContent).toContain('The daily prune job has been disabled since 14 Sep');
    expect(open.textContent).toContain('Blast radius · 13');
    expect(open.textContent).toContain('win-games · VM');
    const primary = open.querySelector<HTMLButtonElement>('.btn.primary')!;
    expect(primary.textContent).toBe('Prune per policyT2');
    // alternatives run too: the engine plans each one
    const alts = [...open.querySelectorAll<HTMLButtonElement>('.acts .btn:not(.primary)')];
    expect(alts.map((b) => [b.textContent, b.disabled])).toEqual([
      ['Re-enable the daily prune jobT1', false],
      ['Open PBST0', false],
    ]);
  });

  it('asks to hold for T2, and runs T1 then shows the resolution in place', async () => {
    const e = engine();
    const hub = fakeFetch(e.routes);
    const { panel, ui, ws, live } = await openOn('pbs-datastore-filling');
    panel.querySelector<HTMLButtonElement>('.ib.open .btn.primary')!.click();
    await flushPromises();
    expect(ui.state.confirm?.action.tier).toBe('T2');
    expect(ui.state.confirm?.plan?.action).toBe('pbs.datastore.prune');
    ui.cancelConfirm();
    expect(e.approvals).toHaveLength(0);

    const titles = [...panel.querySelectorAll<HTMLButtonElement>('.ib-title')];
    titles.find((t) => t.textContent === 'Backup of homeassistant failed')!.click();
    await nextTick();
    const item = titles.find((t) => t.textContent === 'Backup of homeassistant failed')!.closest('.ib')!;
    item.querySelector<HTMLButtonElement>('.btn.primary')!.click();
    await flushPromises();
    // Planned, then approved straight away (T1); never the demo endpoint.
    expect(e.plans.at(-1)).toEqual({ action: 'pve.guest.backup', target: 'pve:heron/qemu/200' });
    expect(e.approvals).toEqual([{ plan: 'p_pve_guest_backup_pve_heron_qemu_200', hash: 'sha256:pve.guest.backup:pve:heron/qemu/200' }]);
    expect(hub.calls.some((c) => c.path.includes('/api/demo/'))).toBe(false);
    expect(live.fixes['backup-failed']?.state).toBe('accepted');
    await nextTick();
    // The task's current step shows where the fix is.
    expect(item.querySelector('.working')?.textContent).toContain('Back up homeassistant now: Ask the hub to back up homeassistant now · 10%');

    ws.push({ op: 'diff', id: ws.subId('inbox'), seq: 4000, patch: [{ op: 'replace', path: toPointer('inbox', 'incidents', 'backup-failed'), value: resolvedIncident('backup-failed') }] });
    await nextTick();
    await nextTick();
    const doneRow = panel.querySelector('.done-row');
    expect(doneRow?.textContent).toContain('The retry at 15:30 succeeded.');
    // after a moment it moves to Resolved
    vi.advanceTimersByTime(3000);
    await nextTick();
    const groups = [...panel.querySelectorAll('.ib-grp')].map((g) => g.textContent?.trim());
    expect(groups).toEqual(['Open · 10', 'Being fixed · 1', 'Security hints · 4', 'Resolved · 1']);
  });

  it('is empty when nothing needs you', async () => {
    // The quiet home still has its standing hints: nothing open, only those.
    const { panel } = await openOn('', docs(healthy));
    expect([...panel.querySelectorAll('.ib-grp')].map((g) => g.textContent?.trim())).toEqual(['Security hints · 4']);
    document.body.innerHTML = '';
    const none = docs(healthy);
    none.inbox.incidents = {};
    const empty = await openOn('', none);
    expect(empty.panel.querySelector('.ib-empty')?.textContent).toContain('Nothing needs you');
  });
});

describe('hold to confirm', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'requestAnimationFrame', 'cancelAnimationFrame', 'performance', 'Date'] });
  });

  it('confirms only after a full hold; letting go early springs back', async () => {
    const w = mount(HoldToConfirm, { props: { label: 'prune', duration: 650 }, attachTo: document.body });
    const btn = w.find('button.hold');
    await btn.trigger('pointerdown');
    vi.advanceTimersByTime(320);
    expect((w.vm as unknown as { progress: number }).progress).toBeGreaterThan(0.3);
    await btn.trigger('pointerup');
    vi.advanceTimersByTime(1500);
    expect(w.emitted('confirm')).toBeUndefined();
    expect((w.vm as unknown as { progress: number }).progress).toBeLessThan(0.05);

    await btn.trigger('pointerdown');
    vi.advanceTimersByTime(700);
    await nextTick();
    expect(btn.classes()).toContain('done');
    vi.advanceTimersByTime(500);
    expect(w.emitted('confirm')).toHaveLength(1);
  });

  it('holds with the keyboard too', async () => {
    const w = mount(HoldToConfirm, { props: { label: 'restart' }, attachTo: document.body });
    const btn = w.find('button.hold');
    await btn.trigger('keydown', { key: ' ' });
    vi.advanceTimersByTime(700);
    await btn.trigger('keyup', { key: ' ' });
    vi.advanceTimersByTime(500);
    expect(w.emitted('confirm')).toHaveLength(1);
  });

  it('lights the blast radius while you hold and runs the action when confirmed', async () => {
    vi.setSystemTime(new Date('2026-10-03T14:05:10Z'));
    const h = harness(docs(withIncidents), { demo: true });
    const e = engine();
    fakeFetch(e.routes);
    h.mountIn(ConfirmDialog);
    const inc = h.live.inbox.value!.incidents['host-memory-pressure']!;
    h.ui.act(inc);
    await flushPromises();
    await nextTick();
    const dialog = document.body.querySelector<HTMLElement>('.confirm')!;
    expect(dialog.querySelector('h3')!.textContent).toBe('Restart files T2');
    // The plan's steps say what will happen.
    expect([...dialog.querySelectorAll('.confirm-steps li')].map((li) => li.textContent)).toEqual(['Ask the hub to restart files', 'Check it worked']);
    const items = [...dialog.querySelectorAll('.blast li')];
    expect(items.length).toBeGreaterThan(1);
    expect(items.filter((li) => li.classList.contains('on'))).toHaveLength(0);
    const hold = dialog.querySelector<HTMLButtonElement>('.hold')!;
    hold.dispatchEvent(new Event('pointerdown'));
    vi.advanceTimersByTime(700);
    await nextTick();
    expect(dialog.querySelectorAll('.blast li.on').length).toBe(items.length);
    vi.advanceTimersByTime(500);
    await flushPromises();
    expect(e.approvals.map((a) => a.plan)).toEqual(['p_pve_guest_restart_pve_stork_lxc_220']);
    expect(h.ui.state.confirm).toBeUndefined();
  });
});
