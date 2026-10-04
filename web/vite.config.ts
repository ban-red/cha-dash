import vue from '@vitejs/plugin-vue';
import { defineConfig } from 'vitest/config';

// The hub serves the built app with a strict CSP (default-src 'self'), so the
// build must not inline anything: no data: URIs for assets, no inline scripts.
export default defineConfig({
  plugins: [vue()],
  // One Vue, whichever workspace package imports it (@chadash/layout-vue too).
  resolve: { dedupe: ['vue'] },
  server: {
    // `bun run dev` (repo root) starts the hub on 127.0.0.1:3696 and this server.
    // 127.0.0.1, not localhost: Node may resolve localhost to ::1, where the hub isn't listening.
    // changeOrigin stays false: the hub's cross-origin checks compare Origin
    // with Host, and both are the dev server's.
    proxy: {
      '/api': { target: 'http://127.0.0.1:3696' },
      '/ws': { target: 'ws://127.0.0.1:3696', ws: true },
    },
  },
  build: {
    // Relative to web/; `go build -tags webui` embeds this directory.
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
    assetsInlineLimit: 0,
    modulePreload: { polyfill: false },
    target: 'es2022',
    reportCompressedSize: true,
    rollupOptions: {
      output: {
        // Name the lazy chunk that carries motion-v (it rides with the Inbox and
        // the confirm dialog) instead of after whichever module it happens to hold.
        chunkFileNames: (chunk) =>
          chunk.moduleIds.some((id) => /[\\/]motion-v[\\/]/.test(id)) ? 'assets/motion-[hash].js' : 'assets/[name]-[hash].js',
      },
    },
  },
  test: {
    environment: 'happy-dom',
    include: ['test/**/*.test.ts'],
    setupFiles: ['test/setup.ts'],
    restoreMocks: true,
  },
});
