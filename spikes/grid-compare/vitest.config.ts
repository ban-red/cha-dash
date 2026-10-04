import vue from '@vitejs/plugin-vue';
import { defineConfig } from 'vitest/config';
import { alias, dedupe } from './vite.shared.ts';

export default defineConfig({
  plugins: [vue()],
  resolve: { alias, dedupe: [...dedupe, '@vue/test-utils'] },
  test: {
    environment: 'happy-dom',
    include: ['test-vue/**/*.test.ts'],
  },
});
