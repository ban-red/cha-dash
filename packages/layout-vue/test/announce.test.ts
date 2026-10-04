import { afterEach, describe, expect, it } from 'vitest';
import { nextTick } from 'vue';
import { homeBoard, key, mountBoard } from './helpers.ts';

afterEach(() => {
  document.body.innerHTML = '';
});

describe('announcements', () => {
  it('speak every keyboard step in a polite live region', async () => {
    const h = mountBoard({ board: homeBoard(), breakpoint: 'desktop', editing: true });
    const region = h.wrapper.element.querySelector('[role="status"]') as HTMLElement;
    expect(region.getAttribute('aria-live')).toBe('polite');
    expect(h.announcement()).toBe('');

    h.item('plex').focus();
    key(h.item('plex'), 'Enter');
    await nextTick();
    expect(h.announcement()).toBe(
      'Picked up Plex at row 3, column 7. Arrow keys move it, Shift and arrow keys resize it, Enter drops it, Escape cancels.',
    );

    key(h.item('plex'), 'ArrowLeft');
    await nextTick();
    expect(h.announcement()).toBe('Moved Plex to row 3, column 5');

    key(h.item('plex'), 'ArrowRight', { shiftKey: true });
    await nextTick();
    expect(h.announcement()).toBe('Resized to Wide');

    key(h.item('plex'), 'Enter');
    await nextTick();
    expect(h.announcement()).toMatch(/^Dropped Plex at row \d+, column \d+$/);
  });

  it('names the section when an item crosses into another one', async () => {
    const h = mountBoard({ board: homeBoard(), breakpoint: 'desktop', editing: true });
    h.item('plex').focus();
    key(h.item('plex'), 'Enter');
    key(h.item('plex'), 'ArrowRight');
    await nextTick();
    expect(h.announcement()).toBe('Moved Plex to Today, row 1, column 1');
  });

  it('says when a move or resize is not possible', async () => {
    const h = mountBoard({ board: homeBoard(), breakpoint: 'desktop', editing: true });
    h.item('pve').focus();
    key(h.item('pve'), 'Enter');
    key(h.item('pve'), 'ArrowLeft');
    await nextTick();
    expect(h.announcement()).toBe("Proxmox can't move further left");
    key(h.item('pve'), 'ArrowRight', { shiftKey: true });
    await nextTick();
    expect(h.announcement()).toBe("Proxmox can't get any wider");
    expect(h.updates).toEqual([]);
  });

  it('re-announces a repeated message as a new node', async () => {
    const h = mountBoard({ board: homeBoard(), breakpoint: 'desktop', editing: true });
    h.item('pve').focus();
    key(h.item('pve'), 'Enter');
    key(h.item('pve'), 'ArrowLeft');
    await nextTick();
    const first = h.wrapper.element.querySelector('[role="status"] span');
    key(h.item('pve'), 'ArrowLeft');
    await nextTick();
    const second = h.wrapper.element.querySelector('[role="status"] span');
    expect(second?.textContent).toBe(first?.textContent);
    expect(second).not.toBe(first);
  });

  it('can be localised through the messages prop', async () => {
    const h = mountBoard({
      board: homeBoard(),
      breakpoint: 'desktop',
      editing: true,
      messages: { resized: (label, size) => `${label} ist jetzt ${size}` },
    });
    h.item('dns').focus();
    key(h.item('dns'), 'ArrowRight', { shiftKey: true });
    await nextTick();
    expect(h.announcement()).toBe('Pi-hole ist jetzt Wide');
  });

  it('links edit-mode instructions to the board and its items', () => {
    const h = mountBoard({ board: homeBoard(), breakpoint: 'desktop', editing: true });
    const root = h.wrapper.element as HTMLElement;
    const id = root.getAttribute('aria-describedby');
    expect(id).toBeTruthy();
    expect(h.item('pve').getAttribute('aria-describedby')).toBe(id);
    expect(document.getElementById(id as string)?.textContent).toMatch(/Enter or Space/);
    expect(root.getAttribute('role')).toBe('group');
    expect(root.getAttribute('aria-label')).toBe('Board layout');
  });
});

describe('external changes', () => {
  it('abandon a running gesture without emitting', async () => {
    const board = homeBoard();
    const h = mountBoard({ board, breakpoint: 'desktop', editing: true });
    h.item('dns').focus();
    key(h.item('dns'), 'Enter');
    key(h.item('dns'), 'ArrowLeft');
    await nextTick();
    expect(h.updates).toHaveLength(1);
    const elsewhere = homeBoard(); // a different object, e.g. from another tab
    await h.wrapper.setProps({ board: elsewhere });
    expect(h.announcement()).toBe('The layout changed, so moving Pi-hole stopped');
    expect(h.item('dns').getAttribute('aria-pressed')).toBe('false');
    key(h.item('dns'), 'ArrowLeft'); // moves focus now; nothing is picked up
    key(h.item('dns'), 'Escape');
    await nextTick();
    expect(h.updates).toHaveLength(1);
    expect(h.commits).toEqual([]);
  });
});
