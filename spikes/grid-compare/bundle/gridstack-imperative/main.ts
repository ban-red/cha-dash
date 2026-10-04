// gridstack driven imperatively from a Vue component, without the wrapper.
import { GridStack, type GridStackOptions } from 'gridstack';
import 'gridstack/dist/gridstack.css';
import { createApp, defineComponent, h, onBeforeUnmount, onMounted, useTemplateRef } from 'vue';
import { constraints } from '../../adapters/gridstack.ts';
import { DOGFOOD, SIZE_UNITS } from '../../scenarios/index.ts';

const options: GridStackOptions = {
  column: 12,
  cellHeight: 'auto',
  mode: 'list',
  subGridOpts: { column: 'auto', mode: 'compact', acceptWidgets: true },
  children: DOGFOOD.sections.map((s) => ({
    id: s.id,
    w: s.span,
    h: 6,
    sizeToContent: true,
    subGridOpts: { children: s.items.map((i) => ({ id: i.id, ...SIZE_UNITS[i.size], ...constraints(i.supportedSizes) })) },
  })),
};

GridStack.renderCB = (el, w) => {
  el.textContent = String(w.id ?? '');
};

const App = defineComponent(() => {
  const root = useTemplateRef<HTMLElement>('root');
  let grid: GridStack | null = null;
  onMounted(() => {
    grid = GridStack.init(options, root.value!);
  });
  onBeforeUnmount(() => grid?.destroy());
  return () => h('div', { ref: 'root', class: 'grid-stack' });
});

createApp(App).mount('#app');
