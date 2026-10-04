// @ts-check
/**
 * Cha Dash design tokens: the generator core.
 *
 * One file, no imports, no platform APIs. `scripts/inject.ts` copies it
 * verbatim (minus the `export` keywords) into `design/prototypes/m0-v2.html`,
 * so the prototype's live theme editor runs exactly this code. ES module
 * imports don't load from `file://` in Chromium, which is why it's inlined.
 * `test/inline.test.ts` fails if the inline copy drifts from this file.
 *
 * What lives here:
 *   1. Colour math: OKLCH <-> sRGB, gamut mapping, hex.
 *   2. Contrast: APCA (Lc, APCA-W3 0.0.98G constants) and WCAG 2 ratios.
 *   3. Colour-vision checks: Machado 2009 CVD simulation and OKLab delta E,
 *      the same model and thresholds as the dataviz palette validator.
 *   4. The theme document validator (structure, with specific messages).
 *   5. The generator: base / accent / contrast -> 12-step OKLCH scales,
 *      semantic colour tokens, type, spacing, radii and motion tokens.
 *   6. Accessibility checks on a resolved theme (status pairs, text contrast).
 *   7. Emitters: CSS custom properties and the JSON export.
 */

export const FORMAT = 1;
export const GENERATOR = 'chadash-tokens 0.1.0';

/* ═══════════════════════════ 0 · Types ═══════════════════════════ */

/** @typedef {{ l: number, c: number, h: number }} Oklch */
/** @typedef {{ l: number, c: number, h: number, alpha: number }} OklchA */
/** @typedef {[number, number, number]} Vec3 */
/** @typedef {{ hue: number, chroma: number, l?: number }} HueSpec */
/**
 * A theme document, as written in `themes/*.yaml`. See `schema/theme.schema.json`.
 * @typedef {{
 *   format: number,
 *   id: string,
 *   name: string,
 *   description?: string,
 *   scheme?: 'light' | 'dark',
 *   profile?: 'standard' | 'dim',
 *   monochrome?: boolean,
 *   color: {
 *     base: string,
 *     accent: string,
 *     contrast: number,
 *     ink?: { hue: number, chroma: number },
 *     status: { attention: HueSpec, down: HueSpec, maintenance: HueSpec },
 *     overrides?: Record<string, string>,
 *   },
 *   typography: { ui: string, numeral: string, clock: string, headline: string },
 *   surface: 'card' | 'ruled' | 'flat',
 *   shape: { radius: number, hairline: number },
 *   density: 'comfortable' | 'compact',
 *   briefing: 'sentence' | 'headline',
 *   material: { frosted: number },
 *   backdrop: 'solid' | 'grain',
 *   figures?: 'tabular' | 'proportional',
 * }} ThemeDoc
 */
/** @typedef {{ path: string, message: string }} DocError */
/**
 * @typedef {{
 *   id: string, label: string, group: 'status' | 'text' | 'chrome',
 *   value: number, target: number, unit: string,
 *   pass: boolean, severity: 'error' | 'warn' | 'info', message: string,
 * }} Check
 */
/**
 * @typedef {{
 *   id: string, name: string, description: string,
 *   scheme: 'light' | 'dark', profile: 'standard' | 'dim', monochrome: boolean,
 *   doc: ThemeDoc,
 *   targets: { t1: number, t2: number, t3: number, floor1: number, floor2: number, floor3: number },
 *   scales: Record<ScaleName, OklchA[]>,
 *   colors: Record<string, OklchA>,
 *   tokens: Record<string, string>,
 *   hex: Record<string, string>,
 *   defaults: { surface: string, density: string, briefing: string, backdrop: string },
 *   notes: string[],
 * }} ResolvedTheme
 */
/** @typedef {'neutral' | 'accent' | 'attention' | 'down' | 'maint'} ScaleName */

/* ═══════════════════════════ 1 · Colour math ═══════════════════════════ */

/** @param {number} x @param {number} lo @param {number} hi */
const clamp = (x, lo, hi) => Math.min(hi, Math.max(lo, x));
/** @param {number} a @param {number} b @param {number} t */
const lerp = (a, b, t) => a + (b - a) * t;
/** Round to `d` decimals, never returning -0. @param {number} x @param {number} d */
const round = (x, d) => { const f = 10 ** d; const v = Math.round(x * f) / f; return v === 0 ? 0 : v; };
/** @param {number} h */
const normHue = (h) => ((h % 360) + 360) % 360;
/** Shortest-path hue interpolation. @param {number} a @param {number} b @param {number} t */
const lerpHue = (a, b, t) => { let d = normHue(b) - normHue(a); if (d > 180) d -= 360; if (d < -180) d += 360; return normHue(a + d * t); };

/** @param {number} c */
const srgbToLinear = (c) => (c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
/** @param {number} c */
const linearToSrgb = (c) => (c <= 0.0031308 ? 12.92 * c : 1.055 * c ** (1 / 2.4) - 0.055);

/** @param {Vec3} rgb linear sRGB @returns {Vec3} OKLab */
function linearToOklab([r, g, b]) {
  const l = Math.cbrt(0.4122214708 * r + 0.5363325363 * g + 0.0514459929 * b);
  const m = Math.cbrt(0.2119034982 * r + 0.6806995451 * g + 0.1073969566 * b);
  const s = Math.cbrt(0.0883024619 * r + 0.2817188376 * g + 0.6299787005 * b);
  return [
    0.2104542553 * l + 0.793617785 * m - 0.0040720468 * s,
    1.9779984951 * l - 2.428592205 * m + 0.4505937099 * s,
    0.0259040371 * l + 0.7827717662 * m - 0.808675766 * s,
  ];
}

/** @param {Vec3} lab OKLab @returns {Vec3} linear sRGB (unclamped) */
function oklabToLinear([L, a, b]) {
  const l = (L + 0.3963377774 * a + 0.2158037573 * b) ** 3;
  const m = (L - 0.1055613458 * a - 0.0638541728 * b) ** 3;
  const s = (L - 0.0894841775 * a - 1.291485548 * b) ** 3;
  return [
    4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
    -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
    -0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s,
  ];
}

/** @param {Oklch} c @returns {Vec3} linear sRGB (unclamped) */
export function oklchToLinear({ l, c, h }) {
  const hr = (h * Math.PI) / 180;
  return oklabToLinear([l, c * Math.cos(hr), c * Math.sin(hr)]);
}

/** @param {Vec3} rgb @param {number} [eps] */
const inGamut = (rgb, eps = 1e-6) => rgb.every((v) => v >= -eps && v <= 1 + eps);

/**
 * Map a colour into sRGB by reducing chroma at constant L and H (binary search).
 * Deterministic and good enough for UI colours; L is what contrast depends on.
 * @param {Oklch} col @returns {Oklch}
 */
export function toGamut(col) {
  const l = clamp(col.l, 0, 1);
  const h = normHue(col.h);
  const c0 = Math.max(0, col.c);
  if (l <= 0) return { l: 0, c: 0, h: 0 };
  if (l >= 1) return { l: 1, c: 0, h: 0 };
  if (inGamut(oklchToLinear({ l, c: c0, h }))) return { l, c: c0, h };
  let lo = 0, hi = c0;
  for (let i = 0; i < 28; i++) {
    const mid = (lo + hi) / 2;
    if (inGamut(oklchToLinear({ l, c: mid, h }))) lo = mid; else hi = mid;
  }
  return { l, c: lo, h };
}

/**
 * Canonicalise a colour: gamut-map, then round to the precision the CSS
 * emitter prints, so the hex we test is the colour the browser renders.
 * @param {Oklch} col @returns {Oklch}
 */
export function canon(col) {
  // colours already within the canonical tolerance skip the search, so canon is idempotent
  const g = inGamut(oklchToLinear(col), 5e-5) ? { l: clamp(col.l, 0, 1), c: Math.max(0, col.c), h: normHue(col.h) } : toGamut(col);
  let l = round(g.l, 4), c = round(g.c, 4), h = c < 0.0005 ? 0 : round(g.h, 2);
  if (c < 0.0005) c = 0;
  // rounding can nudge a gamut-edge colour just outside; pull chroma back in
  while (c > 0 && !inGamut(oklchToLinear({ l, c, h }), 5e-5)) c = round(c - 0.0001, 4);
  return { l, c, h };
}

/** @param {number} v 0..1 */
const hex2 = (v) => Math.round(clamp(v, 0, 1) * 255).toString(16).padStart(2, '0');

/** @param {Oklch} col @returns {string} #rrggbb */
export function toHex(col) {
  const rgb = oklchToLinear(toGamut(col));
  return '#' + rgb.map((v) => hex2(linearToSrgb(clamp(v, 0, 1)))).join('');
}

/** @param {string} hex @returns {Vec3} gamma-encoded sRGB 0..1 */
export function hexToSrgb(hex) {
  const h = hex.replace(/^#/, '');
  /** @param {number} i */
  const ch = (i) => parseInt(h.slice(i, i + 2), 16) / 255;
  return [ch(0), ch(2), ch(4)];
}

/** @param {string} hex @returns {Oklch} */
export function hexToOklch(hex) {
  const [L, a, b] = linearToOklab(/** @type {Vec3} */ (hexToSrgb(hex).map(srgbToLinear)));
  const c = Math.hypot(a, b);
  return { l: L, c, h: c < 1e-4 ? 0 : normHue((Math.atan2(b, a) * 180) / Math.PI) };
}

/**
 * Parse `#rrggbb`, `oklch(L C H)`, `oklch(L% C H)` and `oklch(L C H / A)`.
 * @param {unknown} input @returns {OklchA | null}
 */
export function parseColor(input) {
  if (typeof input !== 'string') return null;
  const s = input.trim().toLowerCase();
  if (/^#[0-9a-f]{6}$/.test(s)) return { ...hexToOklch(s), alpha: 1 };
  const m = /^oklch\(\s*([\d.]+)(%?)\s+([\d.]+)\s+([\d.]+)(?:deg)?\s*(?:\/\s*([\d.]+)(%?)\s*)?\)$/.exec(s);
  if (!m) return null;
  let l = parseFloat(m[1]);
  if (m[2]) l /= 100;
  const c = parseFloat(m[3]);
  const h = parseFloat(m[4]);
  const alpha = m[5] === undefined ? 1 : parseFloat(m[5]) / (m[6] ? 100 : 1);
  if (![l, c, h, alpha].every(Number.isFinite) || l > 1 || alpha > 1) return null;
  return { l, c, h: normHue(h), alpha };
}

/** @param {number} x @param {number} d */
const fmt = (x, d) => String(round(x, d));

/** @param {Oklch & { alpha?: number }} col @returns {string} */
export function formatOklch(col) {
  const a = col.alpha ?? 1;
  return `oklch(${fmt(col.l, 4)} ${fmt(col.c, 4)} ${fmt(col.c === 0 ? 0 : col.h, 2)}${a < 1 ? ` / ${fmt(a, 3)}` : ''})`;
}

/**
 * Composite a translucent colour over an opaque background in gamma sRGB,
 * the way browsers blend. @param {string} fgHex @param {number} alpha @param {string} bgHex
 */
export function composite(fgHex, alpha, bgHex) {
  const f = hexToSrgb(fgHex), b = hexToSrgb(bgHex);
  return '#' + f.map((v, i) => hex2(v * alpha + b[i] * (1 - alpha))).join('');
}

/* ═══════════════════════════ 2 · Contrast ═══════════════════════════ */

/** APCA screen luminance (simple 2.4 exponent, per APCA-W3). @param {string} hex */
function apcaY(hex) {
  const [r, g, b] = hexToSrgb(hex);
  return 0.2126729 * r ** 2.4 + 0.7151522 * g ** 2.4 + 0.072175 * b ** 2.4;
}

/**
 * APCA lightness contrast Lc (APCA-W3 0.0.98G-4g). Positive for dark text on
 * light backgrounds, negative for light on dark. Use Math.abs for thresholds.
 * @param {string} textHex @param {string} bgHex @returns {number}
 */
export function apca(textHex, bgHex) {
  const blkThrs = 0.022, blkClmp = 1.414;
  let txt = apcaY(textHex), bg = apcaY(bgHex);
  txt = txt > blkThrs ? txt : txt + (blkThrs - txt) ** blkClmp;
  bg = bg > blkThrs ? bg : bg + (blkThrs - bg) ** blkClmp;
  if (Math.abs(bg - txt) < 0.0005) return 0;
  if (bg > txt) {
    const sapc = (bg ** 0.56 - txt ** 0.57) * 1.14;
    return sapc < 0.1 ? 0 : (sapc - 0.027) * 100;
  }
  const sapc = (bg ** 0.65 - txt ** 0.62) * 1.14;
  return sapc > -0.1 ? 0 : (sapc + 0.027) * 100;
}

/** WCAG 2 relative luminance. @param {string} hex */
function relLum(hex) {
  const [r, g, b] = hexToSrgb(hex).map(srgbToLinear);
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

/** WCAG 2 contrast ratio, 1..21. @param {string} a @param {string} b */
export function wcag(a, b) {
  const [hi, lo] = [relLum(a), relLum(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

/* ═══════════════════════ 3 · Colour-vision checks ═══════════════════════ */

/* Machado, Oliveira & Fernandes (2009), severity 1.0, applied in linear RGB.
   Same simulation model and thresholds as the dataviz palette validator, whose
   CVD thresholds are calibrated to exactly this model. */
/** @type {Record<'protan' | 'deutan' | 'tritan', number[][]>} */
const MACHADO = {
  protan: [[0.152286, 1.052583, -0.204868], [0.114503, 0.786281, 0.099216], [-0.003882, -0.048116, 1.051998]],
  deutan: [[0.367322, 0.860646, -0.227968], [0.280085, 0.672501, 0.047413], [-0.01182, 0.04294, 0.968881]],
  tritan: [[1.255528, -0.076749, -0.178779], [-0.078411, 0.930809, 0.147602], [0.004733, 0.691367, 0.3039]],
};

/** Validator thresholds (OKLab delta E x100, WCAG ratio, OKLCH). */
export const THRESHOLDS = Object.freeze({
  cvdTarget: 8, cvdFloor: 6, normalFloor: 15, markContrast: 3, chromaFloor: 0.1,
  band: { light: [0.43, 0.77], dark: [0.48, 0.67] },
});

/**
 * OKLab delta E x100 between two colours, optionally under simulated CVD.
 * @param {string} hexA @param {string} hexB @param {'protan' | 'deutan' | 'tritan'} [kind]
 */
export function deltaE(hexA, hexB, kind) {
  /** @param {string} hex @returns {Vec3} */
  const lab = (hex) => {
    let rgb = /** @type {Vec3} */ (hexToSrgb(hex).map(srgbToLinear));
    if (kind) {
      const M = MACHADO[kind];
      rgb = /** @type {Vec3} */ (M.map((row) => clamp(row[0] * rgb[0] + row[1] * rgb[1] + row[2] * rgb[2], 0, 1)));
    }
    return linearToOklab(rgb);
  };
  const a = lab(hexA), b = lab(hexB);
  return 100 * Math.hypot(a[0] - b[0], a[1] - b[1], a[2] - b[2]);
}

/**
 * Separation of a colour pair: normal vision and each CVD simulation.
 * @param {string} a @param {string} b
 */
export function separation(a, b) {
  const protan = deltaE(a, b, 'protan'), deutan = deltaE(a, b, 'deutan'), tritan = deltaE(a, b, 'tritan');
  return { normal: deltaE(a, b), protan, deutan, tritan, cvd: Math.min(protan, deutan, tritan) };
}

/* ═══════════════════════ 4 · Theme document validation ═══════════════════════ */

/** Bundled, open-source font families (02-experience -> Theming). */
export const FONTS = Object.freeze({
  'Instrument Sans': 'sans', 'Inter': 'sans', 'IBM Plex Sans': 'sans', 'Schibsted Grotesk': 'sans',
  'Geist Mono': 'mono', 'JetBrains Mono': 'mono', 'Commit Mono': 'mono',
  'Doto': 'dot', 'Newsreader': 'serif',
});
const FALLBACK = { sans: 'ui-sans-serif, system-ui, sans-serif', mono: 'ui-monospace, monospace', serif: 'Georgia, ui-serif, serif' };

/** Semantic colour tokens a theme may override by name. */
export const OVERRIDABLE = Object.freeze([
  'bg', 'surface', 'surface-2', 'line', 'line-2', 'text', 'text-2', 'text-3',
  'accent', 'attention', 'down', 'maint', 'progress', 'focus', 'spark', 'band', 'hatch',
]);

const ENUMS = {
  scheme: ['light', 'dark'], profile: ['standard', 'dim'], surface: ['card', 'ruled', 'flat'],
  density: ['comfortable', 'compact'], briefing: ['sentence', 'headline'], backdrop: ['solid', 'grain'],
  figures: ['tabular', 'proportional'],
};
const DOC_KEYS = ['$schema', 'format', 'id', 'name', 'description', 'scheme', 'profile', 'monochrome', 'color',
  'typography', 'surface', 'shape', 'density', 'briefing', 'material', 'backdrop', 'figures'];

/**
 * Validate a theme document's structure. Returns specific, path-qualified
 * messages; an empty array means valid. Mirrors schema/theme.schema.json and
 * adds the cross-field rules a schema can't express (scheme vs base lightness).
 * @param {unknown} input @returns {DocError[]}
 */
export function validateThemeDoc(input) {
  /** @type {DocError[]} */
  const errs = [];
  /** @param {string} path @param {string} message */
  const err = (path, message) => { errs.push({ path, message }); };
  /** @param {unknown} v @returns {v is Record<string, any>} */
  const isObj = (v) => typeof v === 'object' && v !== null && !Array.isArray(v);
  /** @param {Record<string, any>} o @param {string} path @param {string[]} allowed */
  const noExtra = (o, path, allowed) => { for (const k of Object.keys(o)) if (!allowed.includes(k)) err(path ? `${path}.${k}` : k, `unknown key "${k}"`); };
  /** @param {unknown} v @param {string} path @param {number} lo @param {number} hi */
  const num = (v, path, lo, hi) => {
    if (typeof v !== 'number' || !Number.isFinite(v)) { err(path, 'must be a number'); return false; }
    if (v < lo || v > hi) { err(path, `must be between ${lo} and ${hi} (got ${v})`); return false; }
    return true;
  };
  /** @param {unknown} v @param {string} path */
  const color = (v, path) => { const c = parseColor(v); if (!c) err(path, 'must be a colour: "#rrggbb" or "oklch(L C H)"'); return c; };
  /** @param {unknown} v @param {string} path @param {keyof typeof ENUMS} key */
  const oneOf = (v, path, key) => { if (!ENUMS[key].includes(/** @type {string} */ (v))) err(path, `must be one of ${ENUMS[key].join(', ')}`); };
  /** @param {unknown} v @param {string} path @param {boolean} withL */
  const hueSpec = (v, path, withL) => {
    if (!isObj(v)) { err(path, 'must be an object with hue and chroma'); return; }
    noExtra(v, path, withL ? ['hue', 'chroma', 'l'] : ['hue', 'chroma']);
    num(v.hue, `${path}.hue`, 0, 360);
    num(v.chroma, `${path}.chroma`, 0, 0.4);
    if (withL && v.l !== undefined) num(v.l, `${path}.l`, 0, 1);
  };

  if (!isObj(input)) return [{ path: '', message: 'a theme document must be an object' }];
  const d = input;
  noExtra(d, '', DOC_KEYS);
  if (d.format !== FORMAT) err('format', `must be ${FORMAT} (this generator reads format ${FORMAT})`);
  if (typeof d.id !== 'string' || !/^[a-z][a-z0-9-]{1,39}$/.test(d.id)) err('id', 'must be lowercase letters, digits and dashes, 2-40 characters, starting with a letter');
  if (typeof d.name !== 'string' || !d.name.trim() || d.name.length > 60) err('name', 'must be a non-empty string of at most 60 characters');
  if (d.description !== undefined && (typeof d.description !== 'string' || d.description.length > 240)) err('description', 'must be a string of at most 240 characters');
  if (d.scheme !== undefined) oneOf(d.scheme, 'scheme', 'scheme');
  if (d.profile !== undefined) oneOf(d.profile, 'profile', 'profile');
  if (d.monochrome !== undefined && typeof d.monochrome !== 'boolean') err('monochrome', 'must be true or false');
  if (d.figures !== undefined) oneOf(d.figures, 'figures', 'figures');

  if (!isObj(d.color)) err('color', 'is required');
  else {
    const c = d.color;
    noExtra(c, 'color', ['base', 'accent', 'contrast', 'ink', 'status', 'overrides']);
    const base = color(c.base, 'color.base');
    color(c.accent, 'color.accent');
    num(c.contrast, 'color.contrast', 0, 100);
    if (c.ink !== undefined) hueSpec(c.ink, 'color.ink', false);
    if (!isObj(c.status)) err('color.status', 'is required (attention, down, maintenance)');
    else {
      noExtra(c.status, 'color.status', ['attention', 'down', 'maintenance']);
      for (const k of ['attention', 'down', 'maintenance']) hueSpec(c.status[k], `color.status.${k}`, true);
    }
    if (c.overrides !== undefined) {
      if (!isObj(c.overrides)) err('color.overrides', 'must be a map of token name to colour');
      else for (const [k, v] of Object.entries(c.overrides)) {
        if (!OVERRIDABLE.includes(k)) err(`color.overrides.${k}`, `is not an overridable token (allowed: ${OVERRIDABLE.join(', ')})`);
        else color(v, `color.overrides.${k}`);
      }
    }
    if (base && d.scheme !== undefined && (base.l < 0.5 ? 'dark' : 'light') !== d.scheme) {
      err('scheme', `says "${d.scheme}" but color.base has lightness ${round(base.l, 3)}, which makes a ${base.l < 0.5 ? 'dark' : 'light'} theme`);
    }
  }

  if (!isObj(d.typography)) err('typography', 'is required');
  else {
    noExtra(d.typography, 'typography', ['ui', 'numeral', 'clock', 'headline']);
    for (const k of ['ui', 'numeral', 'clock', 'headline']) {
      const f = d.typography[k];
      if (typeof f !== 'string' || !(f in FONTS)) err(`typography.${k}`, `must be a bundled font: ${Object.keys(FONTS).join(', ')}`);
    }
  }
  oneOf(d.surface, 'surface', 'surface');
  if (!isObj(d.shape)) err('shape', 'is required (radius, hairline)');
  else { noExtra(d.shape, 'shape', ['radius', 'hairline']); num(d.shape.radius, 'shape.radius', 0, 12); num(d.shape.hairline, 'shape.hairline', 0.5, 2); }
  oneOf(d.density, 'density', 'density');
  oneOf(d.briefing, 'briefing', 'briefing');
  if (!isObj(d.material)) err('material', 'is required (frosted)');
  else { noExtra(d.material, 'material', ['frosted']); num(d.material.frosted, 'material.frosted', 0, 1); }
  oneOf(d.backdrop, 'backdrop', 'backdrop');
  return errs;
}

/* ═══════════════════════════ 5 · The generator ═══════════════════════════ */

/* Lightness offsets from the base for steps 1-10, before the contrast factor.
   1 app background · 2 surface (cards) · 3-5 component states · 6-8 borders · 9-10 solid fills.
   Elevation is lighter in dark mode. In light mode cards are lighter than the
   page (step 2 > step 1) and states go darker from there. */
const OFFSETS = {
  dark: [0, 0.032, 0.07, 0.1, 0.13, 0.165, 0.215, 0.3, 0.35, 0.395],
  light: [0, 0.03, -0.025, -0.05, -0.075, -0.1, -0.14, -0.22, -0.3, -0.335],
};
/* Chroma of chromatic steps 1-8 as a fraction of the solid (step 9). */
const TINT = [0.05, 0.09, 0.16, 0.24, 0.32, 0.4, 0.52, 0.72];
/* Default status lightness per scheme: the M0 validated pairs (02-experience -> Color). */
const STATUS_L = { dark: { attention: 0.74, down: 0.64, maintenance: 0.72 }, light: { attention: 0.66, down: 0.55, maintenance: 0.52 } };
/* APCA targets for text: [primary, secondary, tertiary]. Standard = body Lc 90 / 75, labels Lc 60
   (02-experience -> Accessibility). Dim is Night's profile: a red, low-luminance
   ambient mode where Lc 60+ is physically out of reach; see design/tokens/README.md. */
const TARGETS = { standard: [90, 75, 60], dim: [38, 30, 24] };
const BOOST = [14, 15, 15];

/* WCAG 2 AA floor for text, per profile (dim themes are reported, not gated). */
const WCAG_TEXT = { standard: 4.5, dim: 0 };

/**
 * Solve for the lightness (in the text direction, closest to the backgrounds)
 * whose colour reaches |Lc| >= target and a WCAG ratio >= wcagMin on every
 * background. Both are monotone in L along the search direction.
 * @param {number} target @param {string[]} bgs @param {number} hue @param {number} chroma
 * @param {1 | -1} dir +1 = lighter text (dark scheme), -1 = darker text
 * @param {number} from lightness to search from
 * @param {number} wcagMin
 * @returns {{ col: Oklch, reached: boolean }}
 */
function solveText(target, bgs, hue, chroma, dir, from, wcagMin) {
  /** @param {number} l */
  const at = (l) => canon({ l, c: chroma, h: hue });
  /** @param {number} l */
  const ok = (l) => {
    const hx = toHex(at(l));
    return bgs.every((b) => Math.abs(apca(hx, b)) >= target && wcag(hx, b) >= wcagMin);
  };
  const end = dir > 0 ? 1 : 0;
  if (!ok(end)) return { col: at(end), reached: false };
  let near = from, far = end;
  for (let i = 0; i < 32; i++) {
    const mid = (near + far) / 2;
    if (ok(mid)) far = mid; else near = mid;
  }
  // round in the safe direction so canonical rounding can't drop below target
  let l = dir > 0 ? Math.ceil(far * 1e4) / 1e4 : Math.floor(far * 1e4) / 1e4;
  while (!ok(l) && l !== end) l = clamp(round(l + dir * 0.0001, 4), 0, 1);
  return { col: at(l), reached: true };
}

/**
 * Push a mark colour away from its backgrounds until it clears a WCAG ratio
 * (snap-to-passing: hold the hue, move the lightness). Bounded to +-0.1 L.
 * @param {Oklch} col @param {string[]} bgs @param {number} min @param {1 | -1} dir
 */
function snapContrast(col, bgs, min, dir) {
  let c = canon(col);
  for (let i = 0; i < 20; i++) {
    const hx = toHex(c);
    if (bgs.every((b) => wcag(hx, b) >= min)) return { col: c, moved: round(c.l - col.l, 4) };
    c = canon({ ...c, l: c.l + dir * 0.005 });
  }
  return { col: c, moved: round(c.l - col.l, 4) };
}

/** @param {Oklch} c @param {number} [alpha] @returns {OklchA} */
const A = (c, alpha = 1) => ({ ...c, alpha });

/**
 * Generate a chromatic 12-step scale around a solid (step 9).
 * @param {Oklch} solid @param {number[]} ls lightness of neutral steps 1-8
 * @param {'light' | 'dark'} scheme @param {string[]} bgs @param {number[]} tg [t1, t3]
 * @param {number} wcagMin
 */
function chromaticScale(solid, ls, scheme, bgs, tg, wcagMin) {
  const dir = scheme === 'dark' ? 1 : -1;
  /** @type {Oklch[]} */
  const s = [];
  for (let i = 0; i < 8; i++) s.push(canon({ l: ls[i], c: solid.c * TINT[i], h: solid.h }));
  s.push(canon(solid));
  s.push(canon({ l: solid.l + dir * 0.045, c: solid.c, h: solid.h }));
  const bgL = bgs.map((b) => hexToOklch(b).l);
  const start = dir > 0 ? Math.max(...bgL) : Math.min(...bgL);
  s.push(solveText(tg[1], bgs, solid.h, solid.c * 0.85, dir, start, wcagMin).col);
  s.push(solveText(tg[0], bgs, solid.h, Math.min(solid.c * 0.3, 0.06), dir, start, wcagMin).col);
  return s;
}

/**
 * Spring step response, SwiftUI model (WWDC23): duration is the perceptual
 * duration (sets stiffness), bounce 0 = critically damped.
 * @param {number} duration seconds @param {number} bounce 0..0.9
 */
export function spring(duration, bounce) {
  const omega = (2 * Math.PI) / duration;
  const zeta = 1 - clamp(bounce, 0, 0.9);
  /** displacement from target at t, starting at 1 with velocity v0 (units/s toward target) @param {number} t @param {number} [v0] */
  const state = (t, v0 = 0) => {
    const d0 = -v0;
    if (zeta >= 1) {
      const B = d0 + omega;
      const e = Math.exp(-omega * t);
      return { x: e * (1 + B * t), v: e * (B - omega * (1 + B * t)) };
    }
    const wd = omega * Math.sqrt(1 - zeta * zeta);
    const B = (d0 + zeta * omega) / wd;
    const e = Math.exp(-zeta * omega * t);
    const cos = Math.cos(wd * t), sin = Math.sin(wd * t);
    return { x: e * (cos + B * sin), v: e * (-zeta * omega * (cos + B * sin) + (-wd * sin + B * wd * cos)) };
  };
  // settle: just after the last time |displacement| >= 0.001 (1 ms resolution), rounded up to 10 ms
  let last = 0;
  for (let ms = 1; ms <= duration * 6000; ms++) if (Math.abs(state(ms / 1000).x) >= 0.001) last = ms;
  const settle = Math.max(duration, Math.ceil((last + 1) / 10) / 100);
  const n = 40;
  const pts = Array.from({ length: n + 1 }, (_, i) => round(1 - state((settle * i) / n).x, 4));
  pts[0] = 0; pts[n] = 1;
  return { duration, bounce, omega, zeta, settle, easing: `linear(${pts.join(', ')})`, state };
}

/** Motion tokens (02-experience -> Motion). Durations in seconds. */
export const MOTION = Object.freeze({
  press: { duration: 0.2, bounce: 0 },
  move: { duration: 0.35, bounce: 0 },
  lift: { duration: 0.3, bounce: 0.15 },
  sheet: { duration: 0.45, bounce: 0.08 },
});

/** @param {number} px */
const px = (px) => `${round(px, 2)}px`;

/**
 * Global (theme-independent) tokens: type scale, leading, tracking, spacing,
 * layout, motion. Emitted once on :root.
 * @returns {Record<string, string>}
 */
export function globalTokens() {
  /** @type {Record<string, string>} */
  const t = {};
  // Type: a 1.125 modular scale on a 14px body, rounded to half pixels. Roles, not sizes.
  /** @param {number} step */
  const ts = (step) => px(Math.round(14 * 1.125 ** step * 2) / 2);
  Object.assign(t, {
    '--type-micro': ts(-3), '--type-caption': ts(-2), '--type-small': ts(-1), '--type-body': ts(0),
    '--type-callout': ts(1), '--type-title': ts(2), '--type-title-2': ts(4), '--type-headline': ts(6),
    '--type-value': ts(7), '--type-value-lg': ts(9), '--type-display': ts(11), '--type-clock': ts(12),
    '--type-wall': ts(15), '--type-wall-hero': ts(19),
    '--leading-tight': '1.08', '--leading-snug': '1.24', '--leading-normal': '1.4', '--leading-relaxed': '1.52',
    '--tracking-caps': '0.15em', '--tracking-display': '-0.02em', '--tracking-value': '-0.035em', '--tracking-ui': '-0.006em',
    '--weight-regular': '400', '--weight-medium': '500', '--weight-semibold': '600',
  });
  // Spacing: 4pt scale
  for (const [k, v] of [['0-5', 2], ['1', 4], ['1-5', 6], ['2', 8], ['3', 12], ['4', 16], ['5', 20], ['6', 24], ['8', 32], ['10', 40], ['12', 48], ['16', 64]]) {
    t[`--space-${k}`] = px(/** @type {number} */ (v));
  }
  // Layout (comfortable density)
  Object.assign(t, {
    '--gutter': '12px', '--section-gap': '30px', '--tile-pad-x': '14px', '--tile-pad-y': '12px', '--tile-gap': '7px',
    '--unit-min': '76px', '--unit-max': '96px', '--target-min': '44px',
    // 12 desktop columns at the largest unit; follows density because it's derived
    '--board-max': 'calc(12 * var(--unit-max) + 11 * var(--gutter))',
  });
  // Motion: springs as CSS linear() easings sampled from the physics, plus the two tweens
  for (const [name, m] of Object.entries(MOTION)) {
    const s = spring(m.duration, m.bounce);
    t[`--motion-${name}-duration`] = `${Math.round(s.settle * 1000)}ms`;
    t[`--motion-${name}-easing`] = s.easing;
  }
  t['--motion-value-duration'] = '600ms';
  t['--motion-value-easing'] = 'cubic-bezier(0.22, 1, 0.36, 1)';
  t['--motion-fade-duration'] = '150ms';
  t['--motion-fade-easing'] = 'ease';
  return t;
}

/** Variant blocks keyed by attribute, plus the media-query equivalents. */
export function variantTokens() {
  const motion = Object.keys(MOTION).concat(['value']);
  /** @param {string} dur @param {string} ease */
  const allMotion = (dur, ease) => Object.fromEntries(motion.flatMap((k) => [[`--motion-${k}-duration`, dur], [`--motion-${k}-easing`, ease]]));
  return {
    density: {
      compact: { '--gutter': '8px', '--section-gap': '22px', '--tile-pad-x': '11px', '--tile-pad-y': '9px', '--tile-gap': '5px', '--unit-min': '64px', '--unit-max': '80px' },
    },
    surface: {
      card: { '--tile-bg': 'var(--surface)', '--tile-border-width': 'var(--hairline)', '--tile-border-color': 'var(--line)', '--tile-radius': 'var(--radius-md)', '--tile-pad-inline': 'var(--tile-pad-x)', '--section-rule-width': 'var(--hairline)', '--section-rule-color': 'var(--line)' },
      ruled: { '--tile-bg': 'transparent', '--tile-border-width': 'var(--hairline) 0 0', '--tile-border-color': 'var(--line-2)', '--tile-radius': '0px', '--tile-pad-inline': '1px', '--section-rule-width': '2px', '--section-rule-color': 'var(--text)' },
      flat: { '--tile-bg': 'var(--surface)', '--tile-border-width': '0px', '--tile-border-color': 'transparent', '--tile-radius': 'var(--radius-md)', '--tile-pad-inline': 'var(--tile-pad-x)', '--section-rule-width': 'var(--hairline)', '--section-rule-color': 'var(--line)' },
    },
    motion: {
      reduced: allMotion('var(--motion-fade-duration)', 'var(--motion-fade-easing)'),
      none: allMotion('0.01ms', 'linear'),
    },
    material: {
      solid: { '--glass-bg': 'var(--surface)', '--glass-blur': '0px' },
    },
  };
}

/** The theme-level token names, in emission order (every theme defines all of them). */
export function themeTokenNames() {
  const names = [];
  for (const s of ['neutral', 'accent', 'attention', 'down', 'maint']) for (let i = 1; i <= 12; i++) names.push(`--${s}-${i}`);
  names.push(...SEMANTIC.map(([n]) => `--${n}`));
  names.push('--font-ui', '--font-num', '--font-clock', '--font-head', '--figures-display',
    '--radius-xs', '--radius-sm', '--radius-md', '--radius-lg', '--radius-xl', '--radius-full', '--hairline',
    '--tile-bg', '--tile-border-width', '--tile-border-color', '--tile-radius', '--tile-pad-inline', '--section-rule-width', '--section-rule-color',
    '--glass-bg', '--glass-blur', '--backdrop', '--shadow-lift', '--shadow-overlay');
  return names;
}

/* Semantic colour map: [token, scale or derived source]. Components use only these. */
/** @type {[string, string][]} */
const SEMANTIC = [
  ['bg', 'neutral.1'], ['surface', 'neutral.2'], ['surface-2', 'neutral.3'], ['surface-hover', 'neutral.4'], ['surface-active', 'neutral.5'],
  ['border', 'neutral.6'], ['border-strong', 'neutral.7'],
  ['line', 'ink/line'], ['line-2', 'ink/line2'],
  ['text', 'neutral.12'], ['text-2', 'text2'], ['text-3', 'neutral.11'], ['text-inverse', 'neutral.1'],
  ['accent', 'accent.9'], ['accent-hover', 'accent.10'], ['accent-text', 'accent.11'], ['accent-bg', 'accent.3'], ['accent-line', 'accent.7'], ['on-accent', 'on/accent.9'],
  ['progress', 'accent.9'],
  ['attention', 'attention.9'], ['attention-text', 'attention.11'], ['attention-bg', 'attention.3'], ['attention-line', 'attention.7'], ['on-attention', 'on/attention.9'],
  ['down', 'down.9'], ['down-text', 'down.11'], ['down-bg', 'down.3'], ['down-line', 'down.7'], ['on-down', 'on/down.9'],
  ['maint', 'maint.9'], ['maint-text', 'maint.11'], ['maint-bg', 'maint.3'],
  ['focus', 'text2'],
  ['spark', 'ink/spark'], ['band', 'ink/band'], ['hatch', 'ink/hatch'], ['scrim', 'bg/scrim'],
];

/**
 * Resolve a theme document into scales, semantic colours and CSS tokens.
 * Throws if the document is invalid; call validateThemeDoc first for messages.
 * @param {ThemeDoc} doc @returns {ResolvedTheme}
 */
export function resolveTheme(doc) {
  const errors = validateThemeDoc(doc);
  if (errors.length) throw new Error(`invalid theme "${doc && doc.id}": ${errors.map((e) => `${e.path}: ${e.message}`).join('; ')}`);
  const base = /** @type {OklchA} */ (parseColor(doc.color.base));
  const accent = /** @type {OklchA} */ (parseColor(doc.color.accent));
  const scheme = base.l < 0.5 ? 'dark' : 'light';
  const dir = /** @type {1 | -1} */ (scheme === 'dark' ? 1 : -1);
  const profile = doc.profile ?? 'standard';
  const wmin = WCAG_TEXT[profile];
  const contrast = doc.color.contrast;
  const k = 0.8 + 0.4 * (contrast / 100);
  const boost = Math.max(0, (contrast - 50) / 50);
  const tgBase = TARGETS[profile];
  const tg = tgBase.map((t, i) => t + BOOST[i] * boost);
  const ink = doc.color.ink ?? { hue: base.h, chroma: Math.min(0.03, base.c * 1.6) };
  /** @type {string[]} */
  const notes = [];

  // ── neutral scale
  const offs = OFFSETS[scheme];
  const ls = offs.map((o, i) => clamp(base.l + (i === 0 ? 0 : o * k), 0, 1));
  /** @param {number} t */
  const neutralAt = (t) => ({ c: lerp(base.c, ink.chroma, t), h: base.c < 0.0005 ? ink.hue : lerpHue(base.h, ink.hue, t) });
  /** @type {Oklch[]} */
  const neutral = ls.map((l, i) => canon({ l, ...neutralAt(i / 11) }));
  const bgs = [toHex(neutral[0]), toHex(neutral[1])];
  const start = dir > 0 ? Math.max(neutral[0].l, neutral[1].l) : Math.min(neutral[0].l, neutral[1].l);
  const n11 = solveText(tg[2], bgs, neutralAt(10 / 11).h, neutralAt(10 / 11).c, dir, start, wmin);
  const n12 = solveText(tg[0], bgs, ink.hue, ink.chroma, dir, start, wmin);
  const t2 = solveText(tg[1], bgs, neutralAt(10.5 / 11).h, neutralAt(10.5 / 11).c, dir, start, wmin);
  for (const [nm, r, t] of /** @type {[string, {reached: boolean}, number][]} */ ([['text-3', n11, tg[2]], ['text-2', t2, tg[1]], ['text', n12, tg[0]]])) {
    if (!r.reached) notes.push(`${nm}: Lc ${round(t, 1)} is out of reach on these surfaces; using the extreme lightness`);
  }
  // keep step 11 beyond step 10 so the scale stays monotone in the text direction
  let c11 = n11.col;
  if ((c11.l - neutral[9].l) * dir < 0.04) c11 = canon({ ...c11, l: neutral[9].l + dir * 0.04 });
  neutral.push(c11, n12.col);

  // ── accent and status scales
  const lsTint = ls.slice(0, 8);
  const tgScale = [tg[0], tg[2]];
  const scales = /** @type {Record<ScaleName, Oklch[]>} */ ({ neutral });
  scales.accent = chromaticScale(canon(accent), lsTint, scheme, bgs, tgScale, wmin);
  /** @type {Record<string, { col: Oklch, moved: number }>} */
  const statusSolid = {};
  for (const key of /** @type {const} */ (['attention', 'down', 'maintenance'])) {
    const spec = doc.color.status[key];
    const want = { l: spec.l ?? STATUS_L[scheme][key], c: spec.chroma, h: spec.hue };
    statusSolid[key] = doc.monochrome ? { col: canon(want), moved: 0 } : snapContrast(want, bgs, THRESHOLDS.markContrast, dir);
    if (statusSolid[key].moved) notes.push(`${key}: lightness moved ${statusSolid[key].moved > 0 ? '+' : ''}${statusSolid[key].moved} to clear ${THRESHOLDS.markContrast}:1 on the surfaces`);
  }
  scales.attention = chromaticScale(statusSolid.attention.col, lsTint, scheme, bgs, tgScale, wmin);
  scales.down = chromaticScale(statusSolid.down.col, lsTint, scheme, bgs, tgScale, wmin);
  scales.maint = chromaticScale(statusSolid.maintenance.col, lsTint, scheme, bgs, tgScale, wmin);

  // ── semantic colours
  const inkCol = neutral[11];
  // hairlines: quiet up to contrast 70, then they firm up quickly (High Contrast, prefers-contrast: more)
  const lineA = round(0.045 + 0.09 * (contrast / 100) + 0.12 * Math.max(0, (contrast - 70) / 30), 3);
  /** @type {Record<string, OklchA>} */
  const derived = {
    'text2': A(t2.col),
    'ink/line': A(inkCol, lineA),
    'ink/line2': A(inkCol, round(lineA * 1.9, 3)),
    'ink/spark': A(inkCol, 0.55),
    'ink/band': A(inkCol, scheme === 'dark' ? 0.065 : 0.07),
    'ink/hatch': A(inkCol, scheme === 'dark' ? 0.075 : 0.08),
    'bg/scrim': A(neutral[0], scheme === 'dark' ? 0.6 : 0.5),
  };
  /** @param {Oklch} solid */
  const onColor = (solid) => {
    const hx = toHex(solid);
    return Math.abs(apca(toHex(neutral[0]), hx)) >= Math.abs(apca(toHex(neutral[11]), hx)) ? A(neutral[0]) : A(neutral[11]);
  };
  /** @type {Record<string, OklchA>} */
  const colors = {};
  for (const [name, src] of SEMANTIC) {
    if (src.startsWith('on/')) {
      const [sc, st] = src.slice(3).split('.');
      colors[name] = onColor(scales[/** @type {ScaleName} */ (sc)][Number(st) - 1]);
    } else if (src.includes('.')) {
      const [sc, st] = src.split('.');
      colors[name] = A(scales[/** @type {ScaleName} */ (sc)][Number(st) - 1]);
    } else colors[name] = derived[src];
  }
  for (const [name, value] of Object.entries(doc.color.overrides ?? {})) {
    const c = /** @type {OklchA} */ (parseColor(value));
    colors[name] = { ...canon(c), alpha: c.alpha };
    notes.push(`override: ${name} = ${value}`);
  }
  if (doc.color.overrides?.text) colors['text-inverse'] = colors.bg;

  // ── tokens
  /** @type {Record<string, string>} */
  const tokens = {};
  /** @type {Record<string, string>} */
  const hex = {};
  for (const [s, arr] of Object.entries(scales)) arr.forEach((c, i) => { tokens[`--${s}-${i + 1}`] = formatOklch(c); hex[`--${s}-${i + 1}`] = toHex(c); });
  for (const [name, c] of Object.entries(colors)) { tokens[`--${name}`] = formatOklch(c); if (c.alpha === 1) hex[`--${name}`] = toHex(c); }
  const ty = doc.typography;
  /** @param {string} f */
  const stack = (f) => {
    const kind = /** @type {Record<string, string>} */ (FONTS)[f];
    return `"${f}", ${kind === 'serif' ? FALLBACK.serif : kind === 'sans' ? FALLBACK.sans : FALLBACK.mono}`;
  };
  tokens['--font-ui'] = stack(ty.ui);
  tokens['--font-num'] = stack(ty.numeral);
  tokens['--font-clock'] = ty.clock === ty.numeral ? 'var(--font-num)' : `${stack(ty.clock)}`;
  tokens['--font-head'] = stack(ty.headline);
  tokens['--figures-display'] = (doc.figures ?? 'tabular') === 'tabular' ? 'tabular-nums' : 'proportional-nums';
  const r = doc.shape.radius;
  tokens['--radius-xs'] = px(r * 0.5);
  tokens['--radius-sm'] = px(r);
  tokens['--radius-md'] = px(r * 1.5);
  tokens['--radius-lg'] = px(r * 2.5);
  tokens['--radius-xl'] = px(r * 4);
  tokens['--radius-full'] = r === 0 ? '0px' : '999px';
  tokens['--hairline'] = px(doc.shape.hairline);
  Object.assign(tokens, variantTokens().surface[doc.surface]);
  tokens['--glass-bg'] = formatOklch(A(neutral[1], round(doc.material.frosted, 3)));
  tokens['--glass-blur'] = doc.material.frosted >= 1 ? '0px' : '22px';
  tokens['--backdrop'] = doc.backdrop === 'grain' ? grain(inkCol) : 'none';
  const shadowInk = scheme === 'dark' ? 'oklch(0 0 0 / 0.62)' : formatOklch(A({ l: 0.3, c: 0.03, h: base.h }, 0.3));
  const flat = doc.monochrome;
  tokens['--shadow-lift'] = flat ? 'none' : `0 10px 30px -12px ${shadowInk}`;
  tokens['--shadow-overlay'] = flat ? '0 0 0 1px var(--line-2)' : `0 24px 64px -24px ${shadowInk}`;

  return {
    id: doc.id, name: doc.name, description: doc.description ?? '', scheme, profile, monochrome: !!doc.monochrome, doc,
    targets: { t1: round(tg[0], 1), t2: round(tg[1], 1), t3: round(tg[2], 1), floor1: tgBase[0], floor2: tgBase[1], floor3: tgBase[2] },
    scales: /** @type {Record<ScaleName, OklchA[]>} */ (Object.fromEntries(Object.entries(scales).map(([k2, v]) => [k2, v.map((c) => A(c))]))),
    colors, tokens, hex,
    defaults: { surface: doc.surface, density: doc.density, briefing: doc.briefing, backdrop: doc.backdrop },
    notes,
  };
}

/** Paper grain: a tiny fractal-noise SVG tinted with the ink colour. @param {Oklch} ink */
function grain(ink) {
  const [r, g, b] = hexToSrgb(toHex(ink)).map((v) => round(v, 3));
  const svg = `<svg xmlns='http://www.w3.org/2000/svg' width='160' height='160'><filter id='n'><feTurbulence type='fractalNoise' baseFrequency='.85' numOctaves='3' stitchTiles='stitch'/><feColorMatrix values='0 0 0 0 ${r} 0 0 0 0 ${g} 0 0 0 0 ${b} 0 0 0 .055 0'/></filter><rect width='100%' height='100%' filter='url(%23n)'/></svg>`;
  return `url("data:image/svg+xml,${svg.replace(/</g, '%3C').replace(/>/g, '%3E').replace(/"/g, "'")}")`;
}

const DATA_SVG = /url\("data:image\/svg\+xml,([^"]*)"\)/g;

/** FNV-1a, 32-bit: a short, stable content hash for asset names. @param {string} s */
function fnv1a(s) {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), 0x01000193) >>> 0;
  return h.toString(16).padStart(8, '0');
}

/* ═══════════════════════ 6 · Accessibility checks ═══════════════════════ */

/**
 * Run the status-pair and text-contrast checks on a resolved theme.
 * Errors make a theme invalid (the hub refuses it); warnings are reported.
 * @param {ResolvedTheme} t @returns {{ ok: boolean, checks: Check[], refusal: string | null }}
 */
export function checkTheme(t) {
  /** @type {Check[]} */
  const checks = [];
  const T = THRESHOLDS;
  const hx = t.hex;
  const surfaces = [['bg', hx['--bg']], ['surface', hx['--surface']]];
  /** @param {Omit<Check, 'pass' | 'severity'> & { severity?: Check['severity'] }} c @param {boolean} pass */
  const add = (c, pass) => { checks.push({ severity: 'error', ...c, value: round(c.value, 1), pass }); };
  const att = hx['--attention'], down = hx['--down'], acc = hx['--accent'];

  // Status: attention vs down must separate for every reader (glyph + word ship too, but colour must not lie)
  const sep = separation(att, down);
  if (t.monochrome) {
    add({ id: 'status.monochrome', group: 'status', label: 'Attention vs down', value: sep.normal, target: 0, unit: 'ΔE', severity: 'info',
      message: 'Monochrome theme: state is carried by glyph shape and word only (▲ attention, ■ down), by design' }, true);
  } else {
    add({ id: 'status.cvd', group: 'status', label: 'Attention vs down · colour-blind', value: sep.cvd, target: T.cvdTarget, unit: 'ΔE',
      message: `worst of protan ${round(sep.protan, 1)}, deutan ${round(sep.deutan, 1)}, tritan ${round(sep.tritan, 1)}` }, sep.cvd >= T.cvdTarget);
    add({ id: 'status.normal', group: 'status', label: 'Attention vs down · normal vision', value: sep.normal, target: T.normalFloor, unit: 'ΔE',
      message: 'unsimulated vision, the validator’s hard floor' }, sep.normal >= T.normalFloor);
    for (const [name, col] of [['attention', att], ['down', down]]) {
      const ch = hexToOklch(col).c;
      add({ id: `status.chroma.${name}`, group: 'status', label: `${cap(name)} chroma`, value: ch * 100, target: T.chromaFloor * 100, unit: 'C×100',
        message: 'below 0.10 a hue reads as grey' }, ch >= T.chromaFloor);
    }
    for (const [name, col] of [['progress', acc]]) {
      for (const [other, oc] of [['attention', att], ['down', down]]) {
        const d = deltaE(col, oc);
        add({ id: `status.${name}-${other}`, group: 'status', label: `Accent vs ${other}`, value: d, target: T.normalFloor, unit: 'ΔE',
          message: 'progress uses the accent; it must not read as a status colour' }, d >= T.normalFloor);
      }
    }
    const band = T.band[t.scheme];
    const outBand = [att, down].filter((c) => { const l = hexToOklch(c).l; return l < band[0] || l > band[1]; });
    add({ id: 'status.band', group: 'status', label: 'Lightness band (categorical)', value: outBand.length, target: 0, unit: 'outside', severity: 'info',
      message: outBand.length ? `outside the chart-series band ${band[0]}–${band[1]}; allowed for status colours, which always ship with a glyph and a word` : `inside ${band[0]}–${band[1]}` }, true);
  }
  // Marks against surfaces (WCAG 1.4.11 non-text contrast)
  for (const [name, col] of [['attention', att], ['down', down], ['progress', acc], ['focus', hx['--focus']]]) {
    for (const [sn, sc] of surfaces) {
      const r = wcag(col, sc);
      add({ id: `mark.${name}.${sn}`, group: 'chrome', label: `${cap(name)} on ${sn}`, value: r, target: T.markContrast, unit: ':1',
        severity: t.profile === 'dim' ? 'warn' : 'error', message: 'non-text contrast for glyphs, meters and rings' }, r >= T.markContrast);
    }
  }
  // Text: APCA targets plus WCAG 2 AA
  const tg = t.targets;
  for (const [name, floor, aim] of /** @type {[string, number, number][]} */ ([['text', tg.floor1, tg.t1], ['text-2', tg.floor2, tg.t2], ['text-3', tg.floor3, tg.t3]])) {
    for (const [sn, sc] of surfaces) {
      const lc = Math.abs(apca(hx[`--${name}`], sc));
      add({ id: `text.${name}.${sn}.apca`, group: 'text', label: `${cap(name)} on ${sn}`, value: lc, target: floor, unit: 'Lc',
        severity: t.profile === 'dim' ? 'warn' : 'error', message: aim > floor ? `aiming for Lc ${aim} at this contrast` : 'APCA' }, lc >= floor - 0.05);
      const r = wcag(hx[`--${name}`], sc);
      const wTarget = t.profile === 'dim' ? 3 : 4.5;
      add({ id: `text.${name}.${sn}.wcag`, group: 'text', label: `${cap(name)} on ${sn}`, value: r, target: wTarget, unit: ':1',
        severity: t.profile === 'dim' ? 'warn' : 'error', message: 'WCAG 2 AA' }, r >= wTarget);
    }
  }
  // Text on the raised surface (hover rows, segmented controls): reported, not gated
  for (const name of ['text-2', 'text-3']) {
    const lc = Math.abs(apca(hx[`--${name}`], hx['--surface-2']));
    add({ id: `text.${name}.surface-2.apca`, group: 'text', label: `${cap(name)} on surface-2`, value: lc, target: name === 'text-2' ? tg.floor2 : tg.floor3, unit: 'Lc',
      severity: 'info', message: 'hover and raised states' }, true);
  }
  // Hairlines: visible but quiet. Measured as OKLab lightness difference after
  // compositing, because WCAG ratios compress near black (OLED, Night).
  const loud = t.doc.color.contrast > 70; // high-contrast themes want firm rules, so only the floor applies
  for (const [name, lo, hi0] of /** @type {[string, number, number][]} */ ([['line', 0.05, 0.2], ['line-2', 0.09, 0.4]])) {
    const hi = loud ? 1 : hi0;
    const c = t.colors[name];
    const dl = Math.abs(hexToOklch(composite(toHex(c), c.alpha, hx['--surface'])).l - hexToOklch(hx['--surface']).l) * 100;
    add({ id: `chrome.${name}`, group: 'chrome', label: `${cap(name)} on surface`, value: dl, target: lo * 100, unit: 'ΔL×100', severity: 'warn',
      message: `hairlines must be visible (≥ ${lo * 100}) but quiet (≤ ${hi * 100})` }, dl >= lo * 100 && dl <= hi * 100);
  }

  const failing = checks.filter((c) => !c.pass && c.severity === 'error');
  return { ok: failing.length === 0, checks, refusal: failing.length ? refusalMessage(failing[0]) : null };
}

/** @param {string} s */
const cap = (s) => s.charAt(0).toUpperCase() + s.slice(1);

/** Plain-language reason for refusing a theme. @param {Check} c */
export function refusalMessage(c) {
  if (c.id === 'status.cvd') return `Attention and down are too similar for colour-blind users (ΔE ${c.value}, needs ${c.target}).`;
  if (c.id === 'status.normal') return `Attention and down are too similar to tell apart (ΔE ${c.value}, needs ${c.target}).`;
  if (c.id.startsWith('status.chroma')) return `${c.label.replace(' chroma', '')} is too grey to read as a colour (chroma ${round(c.value / 100, 3)}, needs 0.10).`;
  if (c.id.startsWith('status.progress')) return `The accent looks too much like ${c.id.endsWith('attention') ? 'attention' : 'down'} (ΔE ${c.value}, needs ${c.target}); progress would read as a problem.`;
  if (c.id.startsWith('mark.')) return `${c.label} is too faint for glyphs and meters (${c.value}:1, needs ${c.target}:1).`;
  if (c.group === 'text') return `${c.label} is too low-contrast to read (${c.unit === 'Lc' ? 'Lc ' + c.value : c.value + ':1'}, needs ${c.unit === 'Lc' ? 'Lc ' + c.target : c.target + ':1'}).`;
  return `${c.label} fails (${c.value}, needs ${c.target}).`;
}

/* ═══════════════════════════ 7 · Emitters ═══════════════════════════ */

/** @param {string} sel @param {Record<string, string>} vars @param {string} [extra] @param {string} [indent] */
function block(sel, vars, extra = '', indent = '') {
  const body = Object.entries(vars).map(([k, v]) => `${indent}  ${k}: ${v};`).join('\n');
  return `${indent}${sel} {\n${extra ? `${indent}  ${extra}\n` : ''}${body}\n${indent}}`;
}

/**
 * CSS for one resolved theme: its block, plus a prefers-contrast: more block
 * with the colour tokens regenerated at higher contrast (only those that change).
 * @param {ResolvedTheme} t @param {string} selector @param {ResolvedTheme} [more] the same doc at higher contrast
 */
export function themeCss(t, selector, more) {
  const names = themeTokenNames();
  /** @type {Record<string, string>} */
  const vars = {};
  for (const n of names) vars[n] = t.tokens[n];
  let css = block(selector, vars, `color-scheme: ${t.scheme};`);
  if (more) {
    /** @type {Record<string, string>} */
    const diff = {};
    for (const n of names) if (more.tokens[n] !== t.tokens[n] && /^--(neutral|accent|attention|down|maint|line|text|surface|border|focus|spark|bg)/.test(n)) diff[n] = more.tokens[n];
    if (Object.keys(diff).length) {
      // :root alone would leak into every theme; the default only owns an unthemed root
      const sels = selector.split(',').map((s) => s.trim()).map((s) => (s === ':root' ? ':root:not([data-theme])' : `:root${s}`)).join(', ');
      css += `\n@media (prefers-contrast: more) {\n${block(sels, diff, '', '  ')}\n}`;
    }
  }
  return css;
}

/** The same doc with contrast raised for `prefers-contrast: more`. @param {ThemeDoc} doc */
export function moreContrast(doc) {
  return { ...doc, color: { ...doc.color, contrast: Math.min(100, doc.color.contrast + 40) } };
}

/**
 * Full stylesheet: globals on :root, one block per theme (the first is also
 * the :root default), then the attribute variants and their media queries.
 * Variant selectors use :root[...] so they beat any theme block, including a
 * live custom theme added later in the page.
 * @param {ThemeDoc[]} docs
 */
export function buildCss(docs) {
  const out = [`/* Cha Dash design tokens v0 · generated by ${GENERATOR} from design/tokens/themes/*.yaml.\n   Do not edit: change a theme document or src/core.js and run \`bun run generate\`. */`];
  out.push(block(':root', globalTokens()));
  docs.forEach((doc, i) => {
    const t = resolveTheme(doc);
    const sel = i === 0 ? `:root, [data-theme="${doc.id}"]` : `[data-theme="${doc.id}"]`;
    out.push(`/* ${t.name}${t.description ? ' · ' + t.description : ''} */\n` + themeCss(t, sel, resolveTheme(moreContrast(doc))));
  });
  const v = variantTokens();
  out.push('/* Variants: density, surface, motion, material. */');
  out.push(block(':root[data-density="compact"]', v.density.compact));
  for (const [k, vars] of Object.entries(v.surface)) out.push(block(`:root[data-surface="${k}"]`, vars));
  out.push(block(':root[data-motion="reduced"]', v.motion.reduced));
  out.push(`@media (prefers-reduced-motion: reduce) {\n${block(':root:not([data-motion="full"])', v.motion.reduced, '', '  ')}\n}`);
  out.push(block(':root[data-motion="none"]', v.motion.none));
  out.push(block(':root[data-material="solid"]', v.material.solid));
  out.push(`@media (prefers-reduced-transparency: reduce) {\n${block(':root:root', v.material.solid, '', '  ')}\n}`);
  return out.join('\n\n') + '\n';
}

/**
 * CSS for apps served under a strict CSP: like buildCss, but every inline
 * SVG (Paper's grain) becomes a file next to tokens.css, so `default-src
 * 'self'` never meets a data: URI. Bundlers resolve the relative url() and
 * ship the file. Returns the CSS and the files to write, by name.
 * @param {ThemeDoc[]} docs
 * @returns {{ css: string, assets: Record<string, string> }}
 */
export function buildCssWithAssets(docs) {
  /** @type {Record<string, string>} */
  const assets = {};
  const css = buildCss(docs).replace(DATA_SVG, (_, encoded) => {
    // grain() escapes only < > and #, and swaps " for ', so undo exactly that.
    const svg = encoded.replace(/%3C/g, '<').replace(/%3E/g, '>').replace(/%23/g, '#').replace(/'/g, '"');
    const name = `grain-${fnv1a(svg)}.svg`;
    assets[name] = svg + '\n';
    return `url("${name}")`;
  });
  return { css, assets };
}

/**
 * JSON export: globals, variants, and per theme its inputs, defaults, CSS
 * tokens, sRGB hex for opaque colours, and the check results.
 * @param {ThemeDoc[]} docs
 */
export function buildJson(docs) {
  const motion = Object.fromEntries(Object.entries(MOTION).map(([k, m]) => {
    const s = spring(m.duration, m.bounce);
    return [k, { duration: m.duration, bounce: m.bounce, settle: s.settle, easing: s.easing }];
  }));
  return {
    format: FORMAT,
    generator: GENERATOR,
    defaultTheme: docs[0].id,
    global: globalTokens(),
    motion,
    variants: variantTokens(),
    themes: Object.fromEntries(docs.map((doc) => {
      const t = resolveTheme(doc);
      const c = checkTheme(t);
      return [t.id, {
        name: t.name, description: t.description, scheme: t.scheme, profile: t.profile, monochrome: t.monochrome,
        defaults: t.defaults, targets: t.targets, inputs: doc, tokens: t.tokens, hex: t.hex,
        checks: { ok: c.ok, refusal: c.refusal, results: c.checks }, notes: t.notes,
      }];
    })),
  };
}
