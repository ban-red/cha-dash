import { mount } from '@vue/test-utils';
import { afterEach, expect, it } from 'vitest';
import { nextTick } from 'vue';
import DemoBoard from '../demo/DemoBoard.vue';
import { key } from './helpers.ts';

afterEach(() => {
  document.body.innerHTML = '';
});

it('the demo page edits, commits and undoes', async () => {
  const wrapper = mount(DemoBoard, { attachTo: document.body });
  const root = wrapper.element as HTMLElement;
  const item = (id: string) => root.querySelector<HTMLElement>(`[data-item-id="${id}"]`) as HTMLElement;
  const [edit, undo] = wrapper.findAll('button');
  expect(item('dns').textContent).toContain('Pi-hole');
  await edit?.trigger('click');
  expect(root.querySelector('.layout-board')?.hasAttribute('data-editing')).toBe(true);

  item('dns').focus();
  key(item('dns'), 'ArrowRight', { shiftKey: true });
  await nextTick();
  expect(item('dns').dataset['size']).toBe('wide');
  expect(item('dns').textContent).toContain('wide · 4×2');

  await undo?.trigger('click');
  expect(item('dns').dataset['size']).toBe('small');
});
