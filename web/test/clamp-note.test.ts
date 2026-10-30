/**
 * A tile note too long for its tile: cut to two lines, and opened whole in a
 * dialog. jsdom doesn't lay out text, so the test stands in the heights.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { enableAutoUnmount, flushPromises } from '@vue/test-utils';
import { nextTick } from 'vue';
import ClampText from '../src/components/ClampText.vue';
import GlyphTip from '../src/components/GlyphTip.vue';
import NoteDialog from '../src/components/NoteDialog.vue';
import { axeViolations } from './axe';
import { harness } from './harness';

enableAutoUnmount(afterEach);
afterEach(() => vi.restoreAllMocks());

const LONG = 'Prowlarr: Indexers have no definition and will not work: ExampleTracker. Please remove and (or) re-add to Prowlarr';

function heights(scroll: number, client: number) {
  vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockReturnValue(scroll);
  vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(client);
}

describe('a long tile note', () => {
  it('stays plain text when it fits', async () => {
    heights(34, 34);
    const h = harness();
    const w = h.mountIn(ClampText, { text: 'Two health issues.', title: 'Bazarr' });
    await nextTick();
    expect(w.find('button.clamp').exists()).toBe(false);
    expect(w.find('.clamp-text').text()).toBe('Two health issues.');
  });

  it('is cut, says there’s more, and opens whole in a dialog', async () => {
    heights(68, 34);
    const h = harness();
    const w = h.mountIn(ClampText, { text: LONG, title: 'Prowlarr', glyph: 'attn' });
    await nextTick();
    const b = w.find('button.clamp');
    expect(b.exists()).toBe(true);
    // Named by what it shows (WCAG 2.5.3), the hint for screen readers after it.
    expect(b.find('.clamp-more').text()).toBe('more, show all');
    expect(b.find('.clamp-more .sr').text()).toBe(', show all');
    expect(b.attributes('aria-label')).toBeUndefined();
    expect(b.text()).toBe(`${LONG}more, show all`);
    await b.trigger('click');
    expect(h.ui.state.note).toEqual({ title: 'Prowlarr', text: LONG, glyph: 'attn' });

    const d = h.mountIn(NoteDialog);
    await flushPromises();
    expect(d.find('h2').text()).toBe('Prowlarr');
    expect(d.find('.note-dlg-text').text()).toBe(LONG);
    expect(document.activeElement).toBe(d.find('.note-dlg-text').element);
    expect(await axeViolations()).toEqual([]);
    await d.find('.dlg-foot .btn').trigger('click');
    expect(h.ui.state.note).toBeUndefined();
  });

  it('links to the upstream’s page when it has one: the text when it fits, the dialog when cut', async () => {
    heights(34, 34);
    const h = harness();
    const url = 'https://bazarr.lab.example/system/status';
    const w = h.mountIn(ClampText, { text: '/tv: Directory does not exist', title: 'Bazarr', glyph: 'attn', href: url });
    await nextTick();
    const a = w.find('a.clamp-link');
    expect(a.attributes()).toMatchObject({ href: url, target: '_blank', rel: 'noopener noreferrer' });
    expect(a.text()).toBe('/tv: Directory does not exist ↗');

    heights(68, 34);
    const cut = h.mountIn(ClampText, { text: LONG, title: 'Bazarr', glyph: 'attn', href: url });
    await nextTick();
    await cut.find('button.clamp').trigger('click');
    expect(h.ui.state.note).toEqual({ title: 'Bazarr', text: LONG, glyph: 'attn', href: url });
    const d = h.mountIn(NoteDialog);
    await flushPromises();
    const open = d.find('.dlg-foot a.btn');
    expect(open.attributes()).toMatchObject({ href: url, target: '_blank' });
    expect(open.text()).toBe('Open in Bazarr ↗');
    // The bare tile above sits outside any landmark here, unlike in the app: check the dialog.
    expect(await axeViolations(d.element)).toEqual([]);
  });

  it('a warning glyph opens its warning whole, and names it for screen readers', async () => {
    const h = harness();
    const w = h.mountIn(GlyphTip, { kind: 'attn', text: LONG, title: 'Prowlarr' });
    const b = w.find('button.gtip-btn');
    expect(b.attributes('aria-label')).toBe(`${LONG} Show all`);
    // No Popover API in jsdom: hovering shows nothing, and nothing is left in the page.
    await b.trigger('mouseenter');
    expect(document.querySelector('.gtip')).toBeNull();
    await b.trigger('click');
    expect(h.ui.state.note).toEqual({ title: 'Prowlarr', text: LONG, glyph: 'attn' });
  });
});
