/**
 * Builds each entry in bundle/ on its own with Vite (production, minified,
 * no code splitting) and reports min + gzip sizes. Run: `bun run bundle`.
 * Output goes to dist/bundle/<entry>/ (git-ignored).
 */
import vue from '@vitejs/plugin-vue';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { brotliCompressSync, gzipSync } from 'node:zlib';
import { build } from 'vite';
import { alias, dedupe } from '../vite.shared.ts';

const ENTRIES = [
  ['baseline', 'Vue 3.6 alone (the floor)'],
  ['engine', '@chadash/layout alone, no Vue'],
  ['motion', 'Vue + motion-v springs on motion values (what the renderer uses)'],
  ['chadash', 'Vue + @chadash/layout + @chadash/layout-vue (+ motion-v)'],
  ['gridstack', 'Vue + gridstack 14 with its Vue wrapper'],
  ['gridstack-imperative', 'Vue + gridstack 14, imperative (no wrapper)'],
  ['dndkit', 'Vue + @dnd-kit/dom sortable (drag layer only)'],
] as const;

const root = join(import.meta.dir, '..');

function files(dir: string): string[] {
  return readdirSync(dir).flatMap((f) => (statSync(join(dir, f)).isDirectory() ? files(join(dir, f)) : [join(dir, f)]));
}

export interface BundleSize {
  entry: string;
  label: string;
  js: { min: number; gzip: number; brotli: number };
  css: { min: number; gzip: number; brotli: number };
}

export async function buildAll(): Promise<BundleSize[]> {
  const out: BundleSize[] = [];
  for (const [entry, label] of ENTRIES) {
    const outDir = join(root, 'dist', 'bundle', entry);
    await build({
      configFile: false,
      root,
      logLevel: 'warn',
      plugins: [vue()],
      resolve: { alias, dedupe },
      define: { 'process.env.NODE_ENV': '"production"' },
      build: {
        outDir,
        emptyOutDir: true,
        target: 'es2022',
        minify: true,
        modulePreload: { polyfill: false },
        reportCompressedSize: false,
        rollupOptions: {
          input: join(root, 'bundle', entry, 'main.ts'),
          output: { codeSplitting: false, entryFileNames: '[name].js', assetFileNames: '[name][extname]' },
        },
      },
    });
    const sizes = { js: { min: 0, gzip: 0, brotli: 0 }, css: { min: 0, gzip: 0, brotli: 0 } };
    for (const f of files(outDir)) {
      const kind = f.endsWith('.js') ? 'js' : f.endsWith('.css') ? 'css' : undefined;
      if (!kind) continue;
      const buf = readFileSync(f);
      sizes[kind].min += buf.length;
      sizes[kind].gzip += gzipSync(buf, { level: 9 }).length;
      sizes[kind].brotli += brotliCompressSync(buf).length;
    }
    out.push({ entry, label, ...sizes });
  }
  return out;
}

if (import.meta.main) {
  const sizes = await buildAll();
  const base = sizes.find((s) => s.entry === 'baseline')!;
  const kb = (n: number) => `${(n / 1024).toFixed(1)} kB`;
  console.log('| Entry | JS min | JS gzip | CSS gzip | Total gzip | Over Vue alone |');
  console.log('|---|---:|---:|---:|---:|---:|');
  for (const s of sizes) {
    const total = s.js.gzip + s.css.gzip;
    const over = s.entry === 'baseline' || s.entry === 'engine' ? '—' : `+${kb(total - base.js.gzip - base.css.gzip)}`;
    console.log(`| ${s.label} | ${kb(s.js.min)} | ${kb(s.js.gzip)} | ${kb(s.css.gzip)} | ${kb(total)} | ${over} |`);
  }
}
