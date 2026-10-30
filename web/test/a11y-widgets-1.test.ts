import { expect, it } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { axeViolations } from './axe';
import { docs, withIncidents } from './fixtures';
import { harness } from './harness';
import { axeHooks, axeWidgets, InMain } from './a11y-widgets';

axeHooks();

it('sees what it checks: a planted violation in a mounted tile is caught', async () => {
  const h = harness(docs(withIncidents));
  const w = h.mountIn(InMain, { item: { id: 'fleet', type: 'fleet' }, size: 'large' });
  await flushPromises();
  const root = w.element.parentElement!;
  expect(root.querySelector('.tile')).not.toBeNull();
  expect(await axeViolations(root)).toEqual([]);
  root.querySelector('.tile')!.insertAdjacentHTML('beforeend', '<button type="button"></button>');
  expect((await axeViolations(root)).join('\n')).toContain('button-name');
  w.unmount();
  h.live.stop();
});

axeWidgets(1, 4);
