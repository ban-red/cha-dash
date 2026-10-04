import { createHash } from 'node:crypto';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative, sep } from 'node:path';
import vue from '@vitejs/plugin-vue';
import type { Plugin } from 'vite';
import { defineConfig } from 'vitest/config';

const here = import.meta.dirname;

function filesUnder(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const p = join(dir, name);
    return statSync(p).isDirectory() ? filesUnder(p) : [p];
  });
}

/**
 * Emits /sw.js from sw/service-worker.js with this build's app shell filled
 * in: index.html, the public files, and every JS and CSS file. Its version is
 * a hash of that list (the file names carry content hashes) and the worker's
 * own source, so a new build installs a new worker and an unchanged one
 * doesn't. A file, not an inline script, so the CSP stays strict; the page
 * registers it from src/lib/pwa.ts.
 */
function serviceWorker(): Plugin {
  return {
    name: 'chadash-sw',
    apply: 'build',
    generateBundle(_options, bundle) {
      const built = Object.values(bundle)
        .map((f) => f.fileName)
        .filter((n) => /\.(js|css|svg)$/.test(n))
        .map((n) => `/${n}`);
      const pub = join(here, 'public');
      const statics = filesUnder(pub).map((p) => `/${relative(pub, p).split(sep).join('/')}`);
      const precache = ['/index.html', ...statics, ...built.sort()];
      const template = readFileSync(join(here, 'sw/service-worker.js'), 'utf8');
      const version = createHash('sha256').update(template).update(precache.join('\n')).digest('hex').slice(0, 12);
      const source = template.replace("'__CHADASH_VERSION__'", JSON.stringify(version)).replace('self.__CHADASH_PRECACHE__', JSON.stringify(precache));
      this.emitFile({ type: 'asset', fileName: 'sw.js', source });
    },
  };
}

// The hub serves the built app with a strict CSP (default-src 'self'), so the
// build must not inline anything: no data: URIs for assets, no inline scripts.
export default defineConfig({
  plugins: [vue(), serviceWorker()],
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
