// Vue alone: the floor every other entry is measured against.
import { createApp, defineComponent, h, shallowRef } from 'vue';

const App = defineComponent(() => {
  const n = shallowRef(0);
  return () => h('main', [h('button', { onClick: () => n.value++ }, `Edit ${n.value}`)]);
});

createApp(App).mount('#app');
