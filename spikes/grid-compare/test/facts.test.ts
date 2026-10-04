/**
 * Keyboard, ARIA, motion and dependencies, checked in a headless DOM and in
 * the published files, so the README's claims can be re-run on a new release.
 * Our renderer's side of these checks is in test-vue/ (it needs Vue's compiler).
 */
import { describe, expect, test } from 'bun:test';
import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import { GridstackAdapter } from '../adapters/gridstack.ts';
import { DOGFOOD } from '../scenarios/index.ts';

const nm = join(import.meta.dir, '..', 'node_modules');
const read = (p: string) => readFileSync(join(nm, p), 'utf8');
const pkg = (name: string) => JSON.parse(read(`${name}/package.json`)) as { version: string; license: string; dependencies?: Record<string, string> };

describe('gridstack 14', () => {
  const js = readdirSync(join(nm, 'gridstack/dist')).filter((f) => /^(gridstack|dd-|utils|types).*\.js$/.test(f) && f !== 'gridstack-all.js').map((f) => read(`gridstack/dist/${f}`)).join('\n');
  const vue = readdirSync(join(nm, 'gridstack/dist/vue')).filter((f) => f.endsWith('.js')).map((f) => read(`gridstack/dist/vue/${f}`)).join('\n');
  const css = read('gridstack/dist/gridstack.css');

  test('is 14.0.0, MIT, with no runtime dependencies', () => {
    expect(pkg('gridstack')).toMatchObject({ version: '14.0.0', license: 'MIT' });
    expect(pkg('gridstack').dependencies ?? {}).toEqual({});
  });

  test('ships no ARIA: no roles, no aria-* attributes, in the core or the Vue wrapper', () => {
    expect(/aria-|setAttribute\('role'|tabindex/i.test(js + vue)).toBe(false);
  });

  test('handles keys only during a pointer drag: Escape cancels, R rotates', () => {
    const keys = [...js.matchAll(/e\.key === '([^']+)'/g)].map((m) => m[1]);
    expect(new Set(keys)).toEqual(new Set(['Escape', 'r', 'R']));
  });

  test('items rendered in a DOM are not focusable and ignore arrow keys', () => {
    const g = new GridstackAdapter({ mode: 'compact', sectionMode: 'list' });
    g.load(DOGFOOD);
    const el = document.querySelector('[gs-id="fleet"]') as HTMLElement;
    expect(el.getAttribute('tabindex')).toBeNull();
    expect(el.getAttribute('role')).toBeNull();
    const before = JSON.stringify(g.snapshot());
    for (const key of ['Enter', 'ArrowRight', 'ArrowDown', ' ']) {
      el.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }));
    }
    expect(JSON.stringify(g.snapshot())).toBe(before);
    g.dispose();
  });

  test('motion is a fixed 0.3 s CSS transition of left/top/width/height, with no reduced-motion rule', () => {
    expect(css).toContain('transition: left 0.3s, right 0.3s, top 0.3s, height 0.3s, width 0.3s');
    expect(css.includes('prefers-reduced-motion')).toBe(false);
    expect(js.includes('prefers-reduced-motion')).toBe(false);
    expect(/velocity|momentum|stiffness|damping/i.test(js)).toBe(false); // one comment says 'spring back'
  });

  test('the Vue wrapper keeps the layout inside GridStack: options only seed it, Vue hears about changes through events', () => {
    expect(vue).toContain('never wrap in Vue ref()');
    expect(vue).toContain('grid = GridStack.init(props.options, rootEl)');
    expect(vue).toContain('h(Teleport, { to: target }');
  });
});

describe('dnd-kit/dom 0.5', () => {
  const js = read('@dnd-kit/dom/index.js');

  test('is pre-1.0 and MIT, with five runtime dependencies', () => {
    expect(pkg('@dnd-kit/dom')).toMatchObject({ version: '0.5.0', license: 'MIT' });
    expect(Object.keys(pkg('@dnd-kit/dom').dependencies ?? {}).sort()).toEqual(['@dnd-kit/abstract', '@dnd-kit/collision', '@dnd-kit/geometry', '@dnd-kit/state', 'tslib']);
  });

  test('has the accessible-drag basics built in: tabindex, role, roledescription, describedby, a polite live region', () => {
    for (const s of ['"tabindex", "0"', '"aria-roledescription"', '"aria-describedby"', '"aria-live", "polite"']) expect(js).toContain(s);
  });

  test('moves with the keyboard in fixed 10 px steps, not grid cells', () => {
    expect(js).toMatch(/offset: 10,/);
    expect(js).toContain('start: ["Space", "Enter"]');
  });

  test('drops its drop animation under reduced motion, and has no springs or momentum', () => {
    expect(js).toContain('prefersReducedMotion(getWindow(ctx.feedbackElement)) ? 0');
    expect(/spring|momentum|velocity/i.test(js)).toBe(false);
  });

  test('has no layout engine: no packing, no columns, no sizes', () => {
    expect(/column/i.test(js)).toBe(false);
  });
});
