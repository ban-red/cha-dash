/**
 * WCAG 2 AA for every text/background token pair (TEXT_PAIRS), and 3:1 for
 * marks, in all six themes and their `prefers-contrast: more` variants, which
 * tokens.css also ships.
 */
import { describe, expect, test } from 'bun:test';
import { checkTheme, moreContrast, resolveTheme, TEXT_PAIRS, themeTokenNames, THRESHOLDS, wcag, type ThemeDoc } from '../src/core.js';
import { loadThemes } from '../scripts/themes.ts';

const variants = loadThemes().flatMap((doc) => [
  { name: doc.id, t: resolveTheme(doc) },
  { name: `${doc.id} (prefers-contrast: more)`, t: resolveTheme(moreContrast(doc)) },
]);

describe('every text/background token pair meets WCAG AA', () => {
  for (const { name, t } of variants) {
    test(name, () => {
      const failing = TEXT_PAIRS.map(([fg, bg]) => ({ pair: `${fg} on ${bg}`, ratio: Math.round(wcag(t.hex[`--${fg}`], t.hex[`--${bg}`]) * 100) / 100 })).filter(
        (p) => !(p.ratio >= 4.5),
      );
      expect(failing).toEqual([]);
      for (const mark of ['--attention', '--down', '--progress', '--maint', '--focus']) {
        for (const bg of ['--bg', '--surface']) expect(wcag(t.hex[mark], t.hex[bg])).toBeGreaterThanOrEqual(THRESHOLDS.markContrast);
      }
      const c = checkTheme(t);
      expect(c.checks.filter((x) => !x.pass && x.severity === 'error').map((x) => x.id)).toEqual([]);
    });
  }
});

test('the pairs cover every text token', () => {
  const fgs = new Set(TEXT_PAIRS.map(([fg]) => fg));
  const textTokens = themeTokenNames()
    .map((n) => n.slice(2))
    .filter((n) => /^(text(-\d|-inverse)?|on-.+|.+-text)$/.test(n));
  expect(textTokens.filter((n) => !fgs.has(n))).toEqual([]);
  for (const [fg, bg] of TEXT_PAIRS) {
    expect(themeTokenNames()).toContain(`--${fg}`);
    expect(themeTokenNames()).toContain(`--${bg}`);
  }
});

test('text on a mid-tone solid flips to the end of the scale that reaches AA', () => {
  // Instrument Light's amber: APCA prefers light text, which is 3:1; dark ink clears 4.5:1.
  const t = resolveTheme(loadThemes().find((d) => d.id === 'instrument-light')!);
  expect(wcag(t.hex['--on-attention'], t.hex['--attention'])).toBeGreaterThanOrEqual(4.5);
  expect(wcag(t.hex['--bg'], t.hex['--attention'])).toBeLessThan(4.5);
});

test('a custom theme with an unreadable pair is refused with the pair named', () => {
  const doc = structuredClone(loadThemes()[0]) as ThemeDoc;
  doc.color.overrides = { 'surface-2': 'oklch(0.8 0.01 70)' }; // a raised row nearly as light as the text
  const c = checkTheme(resolveTheme(doc));
  expect(c.ok).toBe(false);
  expect(c.refusal).toMatch(/^Text on surface-2 is too low-contrast to read \([\d.]+:1, needs 4\.5:1\)\.$/);
});
