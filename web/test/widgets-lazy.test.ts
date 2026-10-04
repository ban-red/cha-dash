/**
 * Widgets the default board doesn't use load on demand: a skeleton tile
 * first (no spinner), then the widget, without a reload.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import WidgetHost from '../src/widgets/WidgetHost.vue';
import { docs, withIncidents } from './fixtures';
import { harness } from './harness';

afterEach(() => {
  document.body.innerHTML = '';
});

describe('on-demand widgets', () => {
  it('shows a skeleton until the widget’s code arrives', async () => {
    const { mountIn } = harness(docs(withIncidents));
    const w = mountIn(WidgetHost, { item: { id: 'storage@pbs:main', type: 'storage' }, size: 'small' });
    expect(w.find('article.tile').attributes('aria-busy')).toBe('true');
    expect(w.find('.skel').exists()).toBe(true);
    await vi.dynamicImportSettled();
    await flushPromises();
    expect(w.find('article.tile').attributes('aria-busy')).toBeUndefined();
    expect(w.find('.v').text()).toBe('92% used');
  });
});
