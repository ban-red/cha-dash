// dnd-kit/dom as a drag layer only: sortable tiles in a CSS dense grid.
// It has no layout engine, so a real page would add @chadash/layout on top.
import { DragDropManager } from '@dnd-kit/dom';
import { Sortable } from '@dnd-kit/dom/sortable';
import { createApp, defineComponent, h, onBeforeUnmount, onMounted, shallowRef } from 'vue';
import { DOGFOOD, SIZE_UNITS } from '../../scenarios/index.ts';

const App = defineComponent(() => {
  const items = shallowRef(DOGFOOD.sections.flatMap((s) => s.items));
  const manager = new DragDropManager();
  const sortables: Sortable[] = [];
  onMounted(() => {
    document.querySelectorAll<HTMLElement>('[data-id]').forEach((element, index) => {
      sortables.push(new Sortable({ id: element.dataset['id']!, index, element }, manager));
    });
    manager.monitor.addEventListener('dragend', (event) => {
      const { source } = event.operation;
      if (!source || event.canceled) return;
      const next = [...items.value];
      const from = next.findIndex((i) => i.id === source.id);
      const [moved] = next.splice(from, 1);
      next.splice((source as unknown as { index: number }).index, 0, moved!);
      items.value = next;
    });
  });
  onBeforeUnmount(() => {
    sortables.forEach((s) => s.destroy());
    manager.destroy();
  });
  return () =>
    h(
      'div',
      { style: 'display:grid;grid-template-columns:repeat(12,1fr);grid-auto-flow:row dense' },
      items.value.map((i) =>
        h('div', { key: i.id, 'data-id': i.id, style: `grid-column:span ${SIZE_UNITS[i.size].w};grid-row:span ${SIZE_UNITS[i.size].h}` }, i.id),
      ),
    );
});

createApp(App).mount('#app');
