/**
 * Builds the side-by-side page into one self-contained file,
 * dist/page/index.html, that opens straight from disk (file://): the script
 * and styles are inlined, so the browser fetches nothing. Run: `bun run page`.
 */
import vue from '@vitejs/plugin-vue';
import { fileURLToPath } from 'node:url';
import { defineConfig, type Plugin } from 'vite';
import { alias, dedupe } from '../vite.shared.ts';

function inlineEverything(): Plugin {
  return {
    name: 'grid-compare:inline-everything',
    enforce: 'post',
    generateBundle(_options, bundle) {
      const html = Object.values(bundle).find((f) => f.type === 'asset' && f.fileName.endsWith('.html'));
      if (!html || html.type !== 'asset') return;
      let source = String(html.source);
      for (const [name, file] of Object.entries(bundle)) {
        if (file.type === 'chunk') {
          const code = file.code.replace(/<\/script/gi, '<\\/script');
          source = source.replace(new RegExp(`<script type="module" crossorigin src="[^"]*${file.fileName}"></script>`), () => `<script type="module">${code}</script>`);
          delete bundle[name];
        } else if (file.fileName.endsWith('.css')) {
          source = source.replace(new RegExp(`<link rel="stylesheet" crossorigin href="[^"]*${file.fileName}">`), () => `<style>${String(file.source)}</style>`);
          delete bundle[name];
        }
      }
      html.source = source;
    },
  };
}

export default defineConfig({
  root: fileURLToPath(new URL('.', import.meta.url)),
  base: './',
  plugins: [vue(), inlineEverything()],
  resolve: { alias, dedupe },
  build: {
    outDir: fileURLToPath(new URL('../dist/page', import.meta.url)),
    emptyOutDir: true,
    target: 'es2022',
    assetsInlineLimit: Number.MAX_SAFE_INTEGER,
    modulePreload: { polyfill: false },
    rollupOptions: { output: { codeSplitting: false } },
  },
});
