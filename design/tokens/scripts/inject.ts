/**
 * Inline the token system into the static prototype (design/prototypes/m0-v2.html).
 *
 * ES module imports don't load from file:// in Chromium, so the prototype
 * can't import src/core.js. Instead this script copies three generated blocks
 * into the page between markers:
 *
 *   tokens-css  buildCss() with the grain inline       (inside <style>; dist/tokens.css ships it as a file)
 *   core        src/core.js as an IIFE named ChaTokens  (inside <script>)
 *   themes      themes/*.yaml as THEME_DOCS            (inside <script>)
 *
 * test/inline.test.ts fails if any block drifts from its source.
 *
 *   bun run inject
 */
import { readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { buildCss } from '../src/core.js';
import { PROTOTYPE, ROOT, loadThemes } from './themes.ts';

const MARK = (name: string, edge: 'begin' | 'end') => `/* @generated:${name} ${edge} */`;

/** src/core.js as a classic script: `export` keywords dropped, wrapped in an IIFE. */
export function coreBlock(): string {
  const src = readFileSync(join(ROOT, 'src', 'core.js'), 'utf8');
  const names: string[] = [];
  const body = src.replace(/^export (const|function|let|class) ([A-Za-z_$][\w$]*)/gm, (_m, kind: string, name: string) => {
    names.push(name);
    return `${kind} ${name}`;
  });
  if (/^export /m.test(body)) throw new Error('src/core.js has an export form inject.ts does not handle');
  return [
    '/* Source: design/tokens/src/core.js. Do not edit here; run `bun run inject` in design/tokens. */',
    'const ChaTokens = (() => {',
    body.trimEnd(),
    `return { ${names.join(', ')} };`,
    '})();',
  ].join('\n');
}

/** The built-in theme documents, as plain JSON. */
export function themesBlock(): string {
  const docs = loadThemes().map((d) => {
    const { $schema: _drop, ...rest } = d as typeof d & { $schema?: string };
    return rest;
  });
  return `/* Source: design/tokens/themes/*.yaml. */\nconst THEME_DOCS = ${JSON.stringify(docs, null, 1)};`;
}

/** The prototype is a file:// page with no CSP, so it keeps the grain inline. */
export function cssBlock(): string {
  return buildCss(loadThemes()).trimEnd();
}

/** Replace each marked block in the page. Throws if a marker pair is missing. */
export function inject(html: string): string {
  const blocks: Record<string, string> = { 'tokens-css': cssBlock(), core: coreBlock(), themes: themesBlock() };
  let out = html;
  for (const [name, content] of Object.entries(blocks)) {
    const begin = MARK(name, 'begin'), end = MARK(name, 'end');
    const i = out.indexOf(begin), j = out.indexOf(end);
    if (i < 0 || j < i) throw new Error(`m0-v2.html is missing the ${begin} … ${end} markers`);
    out = out.slice(0, i + begin.length) + '\n' + content + '\n' + out.slice(j);
  }
  return out;
}

/** Extract a marked block's content from the page (for the drift test). */
export function extract(html: string, name: string): string | null {
  const begin = MARK(name, 'begin'), end = MARK(name, 'end');
  const i = html.indexOf(begin), j = html.indexOf(end);
  if (i < 0 || j < i) return null;
  return html.slice(i + begin.length, j).replace(/^\n/, '').replace(/\n$/, '');
}

if (import.meta.main) {
  const html = readFileSync(PROTOTYPE, 'utf8');
  const next = inject(html);
  if (next === html) console.log('m0-v2.html is already up to date');
  else {
    writeFileSync(PROTOTYPE, next);
    console.log('injected tokens-css, core and themes into design/prototypes/m0-v2.html');
  }
}
