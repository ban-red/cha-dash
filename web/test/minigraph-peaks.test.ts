/**
 * The mini graph's peak envelope: a rate's burst that the half-hour
 * averages hide still shows, in the drawing, the readout and the summary.
 */
import { describe, expect, it } from 'vitest';
import { mount } from '@vue/test-utils';
import MiniGraph from '../src/components/marks/MiniGraph.vue';

const values = Array.from({ length: 48 }, () => 20);
const peaks = values.map((v, i) => (i === 24 ? 1000 : v));
const fmt = (v: number) => `${v} Mb/s`;
const box = { left: 0, top: 0, width: 470, height: 30, right: 470, bottom: 30, x: 0, y: 0, toJSON: () => ({}) };

describe('peaks', () => {
  it('draws the envelope and names the peak', async () => {
    const w = mount(MiniGraph, { props: { values, peaks, label: 'WAN', format: fmt }, attachTo: document.body });
    expect(w.find('.mg-peak').attributes('d')).toMatch(/^M.*Z$/);
    expect(w.attributes('aria-label')).toContain('peak 1000 Mb/s');
    w.element.getBoundingClientRect = () => box as DOMRect;
    await w.trigger('pointermove', { clientX: 240 });
    expect(w.find('.mg-read').text()).toBe('20 Mb/s 12 h ago · peak 1000 Mb/s');
    await w.trigger('pointermove', { clientX: 0 });
    expect(w.find('.mg-read').text()).not.toContain('peak'); // no higher than its average
    w.unmount();
  });

  it('draws nothing without peaks above the line', () => {
    const flat = mount(MiniGraph, { props: { values, peaks: values, label: 'WAN' } });
    expect(flat.find('.mg-peak').exists()).toBe(false);
    expect(flat.attributes('aria-label')).not.toContain('peak');
    const none = mount(MiniGraph, { props: { values, label: 'CPU' } });
    expect(none.find('.mg-peak').exists()).toBe(false);
  });
});
