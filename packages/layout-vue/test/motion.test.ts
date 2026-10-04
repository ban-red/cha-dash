import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { nextTick } from 'vue';
import { LIFT_SCALE } from '../src/flip.ts';
import { motionPreference } from '../src/springs.ts';
import { homeBoard, key, mountBoard, pointer, rowBoard, stubGeometry } from './helpers.ts';

let restore: () => void;

beforeEach(() => {
  restore = stubGeometry();
});

afterEach(() => {
  restore();
  document.body.innerHTML = '';
  document.documentElement.removeAttribute('data-motion');
  vi.unstubAllGlobals();
});

const wait = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

describe('motion', () => {
  it('FLIPs reflowed neighbours on springs and settles them', async () => {
    const h = mountBoard({ board: homeBoard(), breakpoint: 'desktop', editing: true, motion: 'full' });
    await nextTick();
    h.item('plex').focus();
    key(h.item('plex'), 'Enter');
    key(h.item('plex'), 'ArrowLeft'); // plex and dns swap places
    await nextTick();
    await nextTick();
    // Inverted synchronously, before paint: dns starts where it was (200 px to the left of its new slot).
    expect(h.item('dns').style.transform).toMatch(/^translate3d\(-200px, 0px, 0\)/);
    await wait(900);
    expect(h.item('dns').style.transform).toMatch(/^translate3d\(0px, 0px, 0\) scale\(0\.982\)$/);
    expect(h.item('plex').style.transform).toMatch(new RegExp(`scale\\(${LIFT_SCALE}\\)$`));
    key(h.item('plex'), 'Enter');
    await wait(700);
    expect(h.item('plex').style.transform).toMatch(/scale\(0\.982\)$/);
  });

  it('holds the dragged item under the pointer, then settles it into its slot', async () => {
    const h = mountBoard({ board: rowBoard(), breakpoint: 'desktop', editing: false, motion: 'full' });
    await h.wrapper.setProps({ editing: true });
    await nextTick();
    pointer(h.item('plex'), 'pointerdown', 50, 50, 0);
    pointer(window, 'pointermove', 90, 70, 100);
    await nextTick();
    // Slot is still (0,0): the offset is the pointer travel.
    expect(h.item('plex').style.transform).toMatch(/^translate3d\(40px, 20px, 0\)/);
    pointer(window, 'pointerup', 90, 70, 200);
    await nextTick();
    await wait(900);
    expect(h.item('plex').style.transform).toMatch(/^translate3d\(0px, 0px, 0\)/);
  });

  it('crossfades instead of moving under reduced motion', async () => {
    document.documentElement.setAttribute('data-motion', 'reduced');
    const h = mountBoard({ board: homeBoard(), breakpoint: 'desktop', editing: true, motion: 'auto' });
    await nextTick();
    h.item('plex').focus();
    key(h.item('plex'), 'Enter');
    key(h.item('plex'), 'ArrowLeft');
    await nextTick();
    await nextTick();
    expect(h.item('dns').style.transform).not.toMatch(/translate3d\(-/);
    expect(Number(h.item('dns').style.opacity)).toBeLessThan(1);
    await wait(400);
    expect(h.item('dns').style.opacity).toBe('');
  });

  it('reads the preference from data-motion, then the OS', () => {
    vi.stubGlobal('matchMedia', (q: string) => ({ matches: q.includes('reduce'), media: q }));
    expect(motionPreference()).toBe('reduced');
    document.documentElement.setAttribute('data-motion', 'full');
    expect(motionPreference()).toBe('full');
    document.documentElement.setAttribute('data-motion', 'none');
    expect(motionPreference()).toBe('none');
    document.documentElement.removeAttribute('data-motion');
    vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q }));
    expect(motionPreference()).toBe('full');
  });
});
