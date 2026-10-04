import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, mount } from '@vue/test-utils';
import { nextTick } from 'vue';
import InboxSheet from '../src/components/InboxSheet.vue';
import HoldToConfirm from '../src/components/HoldToConfirm.vue';
import ConfirmDialog from '../src/components/ConfirmDialog.vue';
import { setDisplayTimeZone } from '../src/lib/format';
import { toPointer } from '../src/lib/jsonpatch';
import { docs, healthy, resolvedIncident, withIncidents } from './fixtures';
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

  async function openOn(id: string, data = docs(withIncidents)) {
    const h = harness(data);
    h.mountIn(InboxSheet);
    h.ui.openInbox(id);
    await flushPromises();
    await nextTick();
    const panel = document.body.querySelector<HTMLElement>('.inbox')!;
    return { ...h, panel };
  }

  it('groups problems and repairs, and opens on the focused incident with facts, causes and blast radius', async () => {
    const { panel } = await openOn('pbs-datastore-filling');
    expect(panel).not.toBeNull();
    const groups = [...panel.querySelectorAll('.ib-grp')].map((g) => g.textContent?.trim());
    expect(groups).toEqual(['Needs you · 4', 'Repairs · 2']);
    const open = panel.querySelector('.ib.open')!;
    expect(open.querySelector('.ib-title')!.textContent).toBe('PBS datastore is filling up');
    expect(open.textContent).toContain('What Cha Dash saw');
    expect(open.textContent).toContain('Used %92');
    expect(open.textContent).toContain('The daily prune job has been disabled since 14 Sep');
    expect(open.textContent).toContain('Blast radius · 14');
    expect(open.textContent).toContain('win-gaming · VM');
    const primary = open.querySelector<HTMLButtonElement>('.btn.primary')!;
    expect(primary.textContent).toBe('Prune per policyT2');
    // alternatives are listed but only the suggested action runs
    expect([...open.querySelectorAll<HTMLButtonElement>('.acts .btn:not(.primary)')].every((b) => b.disabled)).toBe(true);
  });

  it('asks to hold for T2, and runs T1 then shows the resolution in place', async () => {
    const { panel, ui, ws, live } = await openOn('pbs-datastore-filling');
    panel.querySelector<HTMLButtonElement>('.ib.open .btn.primary')!.click();
    expect(ui.state.confirm?.action.tier).toBe('T2');
    ui.cancelConfirm();

    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ id: 'backup-failed' }), { status: 202 }));
    vi.stubGlobal('fetch', fetchMock);
    const titles = [...panel.querySelectorAll<HTMLButtonElement>('.ib-title')];
    titles.find((t) => t.textContent === 'Backup of homeassistant failed')!.click();
    await nextTick();
    const item = titles.find((t) => t.textContent === 'Backup of homeassistant failed')!.closest('.ib')!;
    item.querySelector<HTMLButtonElement>('.btn.primary')!.click();
    await flushPromises();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(live.fixes['backup-failed']?.state).toBe('accepted');
    await nextTick();
    expect(item.querySelector('.working')?.textContent).toContain('working · failed');

    ws.push({ op: 'diff', id: ws.subId('inbox'), seq: 4000, patch: [{ op: 'replace', path: toPointer('inbox', 'incidents', 'backup-failed'), value: resolvedIncident('backup-failed') }] });
    await nextTick();
    await nextTick();
    const doneRow = panel.querySelector('.done-row');
    expect(doneRow?.textContent).toContain('The retry at 15:30 succeeded.');
    // after a moment it moves to Resolved
    vi.advanceTimersByTime(3000);
    await nextTick();
    const groups = [...panel.querySelectorAll('.ib-grp')].map((g) => g.textContent?.trim());
    expect(groups).toEqual(['Needs you · 3', 'Repairs · 2', 'Resolved · 1']);
  });

  it('is empty when nothing needs you', async () => {
    const { panel } = await openOn('', docs(healthy));
    expect(panel.querySelector('.ib-empty')?.textContent).toContain('Nothing needs you');
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
    const h = harness(docs(withIncidents));
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ id: 'host-memory-pressure' }), { status: 202 }));
    vi.stubGlobal('fetch', fetchMock);
    h.mountIn(ConfirmDialog);
    const inc = h.live.inbox.value!.incidents['host-memory-pressure']!;
    h.ui.act(inc);
    await flushPromises();
    const dialog = document.body.querySelector<HTMLElement>('.confirm')!;
    expect(dialog.querySelector('h3')!.textContent).toBe('Restart debian-nas');
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
    expect(fetchMock.mock.calls[0]![0]).toBe('/api/demo/incidents/host-memory-pressure/fix');
    expect(h.ui.state.confirm).toBeUndefined();
  });
});
