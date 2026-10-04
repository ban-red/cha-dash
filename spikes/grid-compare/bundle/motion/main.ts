// Vue + the slice of motion-v the renderer imports (animate, motionValue).
// web/ already ships motion-v for the Inbox and dialogs, so this part of our
// renderer's cost is shared in the real app.
import { animate, motionValue } from 'motion-v';
import { createApp, defineComponent, h, onMounted, useTemplateRef } from 'vue';

const App = defineComponent(() => {
  const el = useTemplateRef<HTMLElement>('el');
  const x = motionValue(0);
  onMounted(() => {
    x.on('change', (v) => el.value && (el.value.style.transform = `translateX(${v}px)`));
    animate(x, 100, { type: 'spring', stiffness: 300, damping: 30, velocity: 0 });
  });
  return () => h('div', { ref: 'el' }, 'tile');
});

createApp(App).mount('#app');
