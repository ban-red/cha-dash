import { describe, expect, test } from 'bun:test';
// apca-w3 ships no types; it's only a reference implementation for parity checks
// @ts-expect-error untyped module
import { APCAcontrast, sRGBtoY } from 'apca-w3';
import {
  apca, canon, deltaE, formatOklch, hexToOklch, hexToSrgb, oklchToLinear, parseColor, separation,
  spring, toGamut, toHex, validateThemeDoc, wcag, type ThemeDoc,
} from '../src/core.js';
import { readThemeFile } from '../scripts/themes.ts';

/** Small deterministic PRNG so the property tests are reproducible. */
function rng(seed: number) {
  return () => {
    seed = (seed + 0x6d2b79f5) | 0;
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}
const randHex = (r: () => number) => '#' + [0, 1, 2].map(() => Math.floor(r() * 256).toString(16).padStart(2, '0')).join('');

describe('colour math', () => {
  test('OKLCH of known sRGB colours', () => {
    const white = hexToOklch('#ffffff');
    expect(white.l).toBeCloseTo(1, 4);
    expect(white.c).toBeLessThan(1e-4);
    const red = hexToOklch('#ff0000');
    expect(red.l).toBeCloseTo(0.628, 3);
    expect(red.c).toBeCloseTo(0.2577, 3);
    expect(red.h).toBeCloseTo(29.23, 1);
  });

  test('hex round-trips through OKLCH', () => {
    const r = rng(7);
    for (let i = 0; i < 500; i++) {
      const hex = randHex(r);
      expect(toHex(hexToOklch(hex))).toBe(hex);
    }
  });

  test('gamut mapping keeps lightness and hue, and lands in sRGB', () => {
    const r = rng(11);
    for (let i = 0; i < 300; i++) {
      const col = { l: r(), c: r() * 0.4, h: r() * 360 };
      const g = toGamut(col);
      expect(g.l).toBeCloseTo(Math.min(1, Math.max(0, col.l)), 9);
      const rgb = oklchToLinear(g);
      for (const v of rgb) {
        expect(v).toBeGreaterThanOrEqual(-1e-6);
        expect(v).toBeLessThanOrEqual(1 + 1e-6);
      }
      if (g.c > 0.001 && g.l > 0 && g.l < 1) expect(g.h).toBeCloseTo(col.h, 6);
    }
  });

  test('canonical colours print and parse back to themselves', () => {
    const r = rng(13);
    for (let i = 0; i < 300; i++) {
      const c = canon({ l: r(), c: r() * 0.3, h: r() * 360 });
      const back = parseColor(formatOklch(c));
      expect(back).not.toBeNull();
      expect(canon(back!)).toEqual(c);
    }
  });

  test('parseColor accepts the documented forms and rejects the rest', () => {
    expect(parseColor('oklch(0.74 0.15 75)')).toEqual({ l: 0.74, c: 0.15, h: 75, alpha: 1 });
    expect(parseColor('oklch(.74 .15 75)')).toEqual({ l: 0.74, c: 0.15, h: 75, alpha: 1 });
    expect(parseColor('oklch(74% 0.15 75deg / 50%)')).toEqual({ l: 0.74, c: 0.15, h: 75, alpha: 0.5 });
    expect(parseColor('#ff0000')?.l).toBeCloseTo(0.628, 3);
    for (const bad of ['red', 'oklch(1.2 0 0)', 'oklch(0.5 0.1)', '#fff', 'rgb(0,0,0)', 42, null]) expect(parseColor(bad)).toBeNull();
  });
});

describe('APCA', () => {
  test('matches the published APCA-W3 reference values', () => {
    expect(apca('#888888', '#ffffff')).toBeCloseTo(63.056469930209424, 9);
    expect(apca('#ffffff', '#888888')).toBeCloseTo(-68.54146436644962, 9);
    expect(apca('#000000', '#ffffff')).toBeCloseTo(106.04067321268862, 6);
    expect(apca('#ffffff', '#000000')).toBeCloseTo(-107.88473318309848, 6);
  });

  test('agrees with the apca-w3 package on random pairs', () => {
    const r = rng(17);
    for (let i = 0; i < 2000; i++) {
      const a = randHex(r), b = randHex(r);
      const ref = Number(APCAcontrast(sRGBtoY(hexToSrgb(a).map((v) => Math.round(v * 255))), sRGBtoY(hexToSrgb(b).map((v) => Math.round(v * 255)))));
      expect(apca(a, b)).toBeCloseTo(ref, 6);
    }
  });
});

describe('WCAG and colour-vision checks', () => {
  test('WCAG ratios', () => {
    expect(wcag('#000000', '#ffffff')).toBeCloseTo(21, 6);
    expect(wcag('#777777', '#ffffff')).toBeCloseTo(4.48, 2);
    expect(wcag('#ffffff', '#ffffff')).toBe(1);
  });

  test('reproduces the M0 status-pair table (02-experience → Color)', () => {
    // A dark: colour-blind 12.2, normal-vision 21.2
    const dark = separation(toHex({ l: 0.74, c: 0.15, h: 75 }), toHex({ l: 0.64, c: 0.21, h: 15 }));
    expect(dark.normal).toBeCloseTo(21.2, 1);
    expect(Math.min(dark.deutan, dark.tritan)).toBeCloseTo(12.2, 1);
    // Orange vs red failed: normal-vision below the floor of 15
    const orange = separation(toHex({ l: 0.7, c: 0.17, h: 50 }), toHex({ l: 0.64, c: 0.21, h: 15 }));
    expect(orange.normal).toBeLessThan(15);
  });

  test('delta E is symmetric and zero for identical colours', () => {
    expect(deltaE('#123456', '#123456')).toBe(0);
    expect(deltaE('#123456', '#abcdef', 'deutan')).toBeCloseTo(deltaE('#abcdef', '#123456', 'deutan'), 12);
  });
});

describe('springs', () => {
  test('every spring settles at 1 and starts at 0', () => {
    for (const [d, b] of [[0.2, 0], [0.35, 0], [0.3, 0.15], [0.45, 0.08], [0.6, 0.4]]) {
      const s = spring(d, b);
      expect(Math.abs(s.state(s.settle).x)).toBeLessThan(0.001);
      expect(s.easing.startsWith('linear(0, ')).toBe(true);
      expect(s.easing.endsWith(', 1)')).toBe(true);
      expect(s.settle).toBeGreaterThanOrEqual(d);
    }
  });

  test('bounce 0 never overshoots; bounce 0.15 overshoots slightly', () => {
    const crit = spring(0.35, 0);
    const lift = spring(0.3, 0.15);
    let maxCrit = 0, maxLift = 0;
    for (let ms = 0; ms <= 2000; ms++) {
      maxCrit = Math.max(maxCrit, 1 - crit.state(ms / 1000).x);
      maxLift = Math.max(maxLift, 1 - lift.state(ms / 1000).x);
    }
    expect(maxCrit).toBeLessThanOrEqual(1);
    expect(maxLift).toBeGreaterThan(1);
    expect(maxLift).toBeLessThan(1.02);
  });

  test('initial velocity carries the motion forward', () => {
    const s = spring(0.45, 0.08);
    expect(s.state(0.05, 8).x).toBeLessThan(s.state(0.05, 0).x);
  });
});

describe('theme document validation', () => {
  const good = () => structuredClone(readThemeFile('instrument-dark.yaml')) as ThemeDoc;

  test('the default theme is valid', () => {
    expect(validateThemeDoc(good())).toEqual([]);
  });

  test('specific, path-qualified messages', () => {
    const d = good() as any;
    d.colour = {};
    d.scheme = 'light';
    d.typography.ui = 'Comic Sans';
    d.color.contrast = 140;
    d.color.status.attention.hue = 'amber';
    d.color.overrides = { glow: '#ffffff', text: 'not a colour' };
    const errs = validateThemeDoc(d);
    const paths = errs.map((e) => e.path);
    expect(paths).toContain('colour');
    expect(paths).toContain('scheme');
    expect(paths).toContain('typography.ui');
    expect(paths).toContain('color.contrast');
    expect(paths).toContain('color.status.attention.hue');
    expect(paths).toContain('color.overrides.glow');
    expect(paths).toContain('color.overrides.text');
    expect(errs.find((e) => e.path === 'scheme')!.message).toContain('lightness 0.175');
    expect(errs.find((e) => e.path === 'color.contrast')!.message).toContain('between 0 and 100');
  });

  test('rejects non-objects and wrong format versions', () => {
    expect(validateThemeDoc(null)[0].message).toContain('must be an object');
    const d = good() as any;
    d.format = 2;
    expect(validateThemeDoc(d).map((e) => e.path)).toEqual(['format']);
  });
});
