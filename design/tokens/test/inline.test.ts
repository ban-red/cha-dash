import { describe, expect, test } from 'bun:test';
import { existsSync, readFileSync } from 'node:fs';
import vm from 'node:vm';
import { coreBlock, cssBlock, extract, inject, themesBlock } from '../scripts/inject.ts';
import { PROTOTYPE, loadThemes } from '../scripts/themes.ts';
import * as core from '../src/core.js';

describe('prototype inline copies (design/prototypes/m0-v2.html)', () => {
  test('the prototype exists', () => {
    expect(existsSync(PROTOTYPE)).toBe(true);
  });

  const html = existsSync(PROTOTYPE) ? readFileSync(PROTOTYPE, 'utf8') : '';

  test('the inline generator core matches src/core.js (run `bun run inject`)', () => {
    expect(extract(html, 'core')).toBe(coreBlock());
  });

  test('the inline tokens.css matches dist/tokens.css', () => {
    expect(extract(html, 'tokens-css')).toBe(cssBlock());
  });

  test('the inline theme documents match themes/*.yaml', () => {
    expect(extract(html, 'themes')).toBe(themesBlock());
  });

  test('injecting is a no-op on an up-to-date page', () => {
    expect(inject(html)).toBe(html);
  });

  test('the inline core runs as a classic script and produces identical tokens', () => {
    const ctx: Record<string, unknown> = {};
    vm.runInNewContext(`${coreBlock()}\n${themesBlock()}\nthis.ChaTokens = ChaTokens; this.THEME_DOCS = THEME_DOCS;`, ctx);
    const T = ctx.ChaTokens as typeof core;
    const docs = ctx.THEME_DOCS as core.ThemeDoc[];
    expect(docs.map((d) => d.id)).toEqual(loadThemes().map((d) => d.id));
    expect(T.buildCss(docs)).toBe(core.buildCss(loadThemes()));
  });
});
