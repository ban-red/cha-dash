import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import Ajv2020 from 'ajv/dist/2020.js';
import {
  apca, buildCss, buildCssWithAssets, checkTheme, moreContrast, resolveTheme, separation, themeTokenNames, THRESHOLDS, wcag,
  globalTokens, validateThemeDoc, type ThemeDoc,
} from '../src/core.js';
import { generate } from '../scripts/generate.ts';
import { DIST_DIR, ROOT, THEME_ORDER, loadThemes, readThemeFile } from '../scripts/themes.ts';

const docs = loadThemes();
const resolved = docs.map((d) => resolveTheme(d));
const byId = Object.fromEntries(resolved.map((t) => [t.id, t]));

describe('theme documents', () => {
  test('all six built-in themes exist, in canonical order', () => {
    expect(docs.map((d) => d.id)).toEqual([...THEME_ORDER]);
    expect(docs.map((d) => d.name)).toEqual(['Instrument Dark', 'Instrument Light', 'OLED', 'Night', 'High Contrast', 'Paper']);
  });

  test('every document passes the JSON Schema', () => {
    const schema = JSON.parse(readFileSync(join(ROOT, 'schema', 'theme.schema.json'), 'utf8'));
    const ajv = new Ajv2020({ allErrors: true, strict: true });
    const validate = ajv.compile(schema);
    for (const id of THEME_ORDER) {
      const ok = validate(readThemeFile(`${id}.yaml`));
      expect({ id, errors: ok ? null : validate.errors }).toEqual({ id, errors: null });
    }
  });

  test('the JSON Schema and the core validator agree on broken documents', () => {
    const schema = JSON.parse(readFileSync(join(ROOT, 'schema', 'theme.schema.json'), 'utf8'));
    const validate = new Ajv2020({ allErrors: true, strict: true }).compile(schema);
    const mutations: [string, (d: any) => void][] = [
      ['unknown key', (d) => { d.wallpaper = 'photo.jpg'; }],
      ['bad font', (d) => { d.typography.numeral = 'Comic Sans'; }],
      ['bad surface', (d) => { d.surface = 'glass'; }],
      ['contrast out of range', (d) => { d.color.contrast = -1; }],
      ['missing status', (d) => { delete d.color.status.down; }],
      ['bad colour', (d) => { d.color.accent = 'blue'; }],
      ['bad id', (d) => { d.id = 'Instrument Dark'; }],
    ];
    for (const [name, mutate] of mutations) {
      const d = structuredClone(readThemeFile('instrument-dark.yaml')) as any;
      mutate(d);
      expect({ name, schema: validate(d) }).toEqual({ name, schema: false });
      expect({ name, core: validateThemeDoc(d).length > 0 }).toEqual({ name, core: true });
    }
  });
});

describe('generated output', () => {
  test('is deterministic', () => {
    const a = generate(), b = generate();
    expect(a.css).toBe(b.css);
    expect(a.json).toBe(b.json);
    expect(buildCssWithAssets(docs).css).toBe(a.css);
  });

  test('ships no data: URIs; the grain is a file next to tokens.css', () => {
    const out = generate();
    expect(out.css).not.toContain('data:');
    const names = Object.keys(out.assets);
    expect(names.length).toBeGreaterThan(0);
    for (const n of names) {
      expect(out.css).toContain(`url("${n}")`);
      expect(out.assets[n]).toStartWith('<svg');
      expect(readFileSync(join(DIST_DIR, n), 'utf8')).toBe(out.assets[n]);
    }
  });

  test('dist/ is up to date (run `bun run generate`)', () => {
    const out = generate();
    expect(readFileSync(join(DIST_DIR, 'tokens.css'), 'utf8')).toBe(out.css);
    expect(readFileSync(join(DIST_DIR, 'tokens.json'), 'utf8')).toBe(out.json);
  });

  test('every theme defines every theme token', () => {
    const names = themeTokenNames();
    expect(new Set(names).size).toBe(names.length);
    for (const t of resolved) {
      const missing = names.filter((n) => !(n in t.tokens) || !t.tokens[n]);
      expect({ id: t.id, missing }).toEqual({ id: t.id, missing: [] });
    }
  });

  test('every theme block in tokens.css declares exactly the theme token set', () => {
    const css = readFileSync(join(DIST_DIR, 'tokens.css'), 'utf8');
    const names = [...themeTokenNames()].sort();
    for (const id of THEME_ORDER) {
      const m = new RegExp(`^(?::root, )?\\[data-theme="${id}"\\] \\{\\n([\\s\\S]*?)\\n\\}`, 'm').exec(css);
      expect(m).not.toBeNull();
      const declared = [...m![1].matchAll(/^\s+(--[\w-]+):/gm)].map((x) => x[1]).sort();
      expect({ id, declared }).toEqual({ id, declared: names });
      expect(m![1]).toContain('color-scheme:');
    }
  });

  test('the default theme also owns :root, and globals are on :root', () => {
    const css = readFileSync(join(DIST_DIR, 'tokens.css'), 'utf8');
    expect(css).toContain(':root, [data-theme="instrument-dark"] {');
    for (const k of Object.keys(globalTokens())) expect(css).toContain(`  ${k}: `);
    expect(css).toContain('@media (prefers-reduced-motion: reduce)');
    expect(css).toContain('@media (prefers-reduced-transparency: reduce)');
    expect(css).toContain('@media (prefers-contrast: more)');
    // the default theme's contrast variant must not leak into other themes
    expect(css).not.toContain(':root:root, :root[data-theme');
  });
});

describe('status pairs pass the palette validator on every theme surface', () => {
  for (const t of resolved) {
    test(`${t.id} (${t.scheme})`, () => {
      const att = t.hex['--attention'], down = t.hex['--down'];
      for (const s of ['--bg', '--surface']) {
        expect(wcag(att, t.hex[s])).toBeGreaterThanOrEqual(THRESHOLDS.markContrast);
        expect(wcag(down, t.hex[s])).toBeGreaterThanOrEqual(THRESHOLDS.markContrast);
      }
      if (t.monochrome) {
        // Only monochrome themes may skip separation; state is glyph + word there.
        expect(t.id).toBe('night');
        return;
      }
      const sep = separation(att, down);
      expect(sep.protan).toBeGreaterThanOrEqual(THRESHOLDS.cvdTarget);
      expect(sep.deutan).toBeGreaterThanOrEqual(THRESHOLDS.cvdTarget);
      expect(sep.tritan).toBeGreaterThanOrEqual(THRESHOLDS.cvdTarget);
      expect(sep.normal).toBeGreaterThanOrEqual(THRESHOLDS.normalFloor);
      // and progress (the accent) never reads as a status colour
      expect(separation(t.hex['--accent'], att).normal).toBeGreaterThanOrEqual(THRESHOLDS.normalFloor);
      expect(separation(t.hex['--accent'], down).normal).toBeGreaterThanOrEqual(THRESHOLDS.normalFloor);
    });
  }

  test('every theme passes its own checks', () => {
    for (const t of resolved) {
      const r = checkTheme(t);
      expect({ id: t.id, refusal: r.refusal }).toEqual({ id: t.id, refusal: null });
    }
  });

  test('orange vs crimson is refused with a specific reason', () => {
    const d = structuredClone(docs[0]) as ThemeDoc;
    d.color.status.attention = { hue: 45, chroma: 0.17 };
    const r = checkTheme(resolveTheme(d));
    expect(r.ok).toBe(false);
    expect(r.refusal).toMatch(/attention and down are too similar/i);
  });

  test('an amber accent is refused because progress would read as attention', () => {
    const d = structuredClone(docs[0]) as ThemeDoc;
    d.color.accent = 'oklch(0.76 0.14 78)';
    const r = checkTheme(resolveTheme(d));
    expect(r.ok).toBe(false);
    expect(r.refusal).toMatch(/accent looks too much like attention/i);
  });
});

describe('text contrast', () => {
  for (const t of resolved) {
    test(`${t.id}: APCA and WCAG on bg and surface`, () => {
      const floors = t.profile === 'dim' ? [38, 30, 24] : [90, 75, 60];
      ['--text', '--text-2', '--text-3'].forEach((tok, i) => {
        for (const s of ['--bg', '--surface']) {
          expect(Math.abs(apca(t.hex[tok], t.hex[s]))).toBeGreaterThanOrEqual(floors[i] - 0.05);
          expect(wcag(t.hex[tok], t.hex[s])).toBeGreaterThanOrEqual(4.5); // WCAG AA in every profile, dim included
        }
      });
      // text steps are ordered: text > text-2 > text-3 in contrast. In the dim
      // profile the AA floor lifts text-2 and text-3 to about the same level.
      const lc = (tok: string) => Math.abs(apca(t.hex[tok], t.hex['--surface']));
      expect(lc('--text')).toBeGreaterThan(lc('--text-2'));
      if (t.profile === 'dim') expect(lc('--text-2')).toBeGreaterThanOrEqual(lc('--text-3'));
      else expect(lc('--text-2')).toBeGreaterThan(lc('--text-3'));
    });
  }

  test('steps 11 and 12 hit Lc 60 and Lc 90 on step 2 (standard themes)', () => {
    for (const t of resolved.filter((x) => x.profile === 'standard')) {
      expect(Math.abs(apca(t.hex['--neutral-11'], t.hex['--neutral-2']))).toBeGreaterThanOrEqual(60 - 0.05);
      expect(Math.abs(apca(t.hex['--neutral-12'], t.hex['--neutral-2']))).toBeGreaterThanOrEqual(90 - 0.05);
    }
  });

  test('prefers-contrast: more never lowers text contrast', () => {
    for (const doc of docs) {
      const a = resolveTheme(doc), b = resolveTheme(moreContrast(doc));
      for (const tok of ['--text', '--text-2', '--text-3']) {
        expect(Math.abs(apca(b.hex[tok], b.hex['--surface']))).toBeGreaterThanOrEqual(Math.abs(apca(a.hex[tok], a.hex['--surface'])) - 0.5);
      }
    }
  });

  // The live editor exposes base lightness/hue/tint, accent hue/chroma and contrast.
  const randomDoc = (r: () => number, L: number) => {
    const d = structuredClone(L < 0.5 ? byId['instrument-dark'].doc : byId['instrument-light'].doc) as ThemeDoc;
    d.color.base = `oklch(${L.toFixed(3)} ${(r() * 0.03).toFixed(3)} ${(r() * 360).toFixed(1)})`;
    d.color.accent = `oklch(${L < 0.5 ? 0.78 : 0.52} ${(r() * 0.12).toFixed(3)} ${(r() * 360).toFixed(1)})`;
    d.color.contrast = Math.round(r() * 100);
    delete d.color.ink;
    return d;
  };
  const textOk = (d: ThemeDoc) => {
    const t = resolveTheme(d);
    return ([['--text', 90], ['--text-2', 75], ['--text-3', 60]] as const)
      .every(([tok, floor]) => Math.min(...['--bg', '--surface'].map((s) => Math.abs(apca(t.hex[tok], t.hex[s])))) >= floor - 0.05);
  };

  test('custom themes in the reachable ranges meet every text floor by construction', () => {
    let seed = 99;
    const r = () => { seed = (seed * 16807) % 2147483647; return seed / 2147483647; };
    for (let i = 0; i < 120; i++) {
      // Lc 90 needs a dark base (L <= ~0.35) or a near-white one (L >= ~0.92)
      const d = randomDoc(r, r() < 0.5 ? r() * 0.3 : 0.93 + r() * 0.07);
      expect({ base: d.color.base, ok: textOk(d) }).toEqual({ base: d.color.base, ok: true });
    }
  });

  test('any custom theme either meets the floors or is refused with a reason', () => {
    let seed = 7;
    const r = () => { seed = (seed * 16807) % 2147483647; return seed / 2147483647; };
    let refused = 0;
    for (let i = 0; i < 150; i++) {
      const d = randomDoc(r, r());
      const c = checkTheme(resolveTheme(d));
      if (!textOk(d)) {
        expect({ base: d.color.base, ok: c.ok }).toEqual({ base: d.color.base, ok: false });
        expect(c.refusal).toBeTruthy();
        refused++;
      }
    }
    expect(refused).toBeGreaterThan(0); // the mid-lightness band really is unreachable
  });
});

describe('dataviz validator parity', () => {
  // The dataviz skill's validator lives outside this repo. Point at it to
  // cross-check: DATAVIZ_VALIDATOR=/path/to/validate_palette.js bun test
  const path = process.env.DATAVIZ_VALIDATOR;
  test.skipIf(!path)('our separation and contrast numbers match validate_palette.js', async () => {
    const mod = await import(path!);
    for (const t of resolved.filter((x) => !x.monochrome)) {
      const att = t.hex['--attention'], down = t.hex['--down'];
      const { report } = mod.validate([att, down], { mode: t.scheme, surface: t.hex['--surface'] });
      const row = (name: string) => report.find((r: [string]) => r[0] === name);
      const cvd = Number(/ΔE ([\d.]+)/.exec(row('CVD separation')[2])![1]);
      const normal = Number(/ΔE ([\d.]+)/.exec(row('Normal-vision floor')[2])![1]);
      const sep = separation(att, down);
      expect(cvd).toBeCloseTo(Math.min(sep.protan, sep.deutan), 1);
      expect(normal).toBeCloseTo(sep.normal, 1);
      expect(row('CVD separation')[1]).toBe('pass');
      expect(row('Normal-vision floor')[1]).toBe('pass');
      expect(row('Contrast vs surface')[1]).toBe('pass');
      expect(row('Chroma floor')[1]).toBe(true);
    }
  });
});
