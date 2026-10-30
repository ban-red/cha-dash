/**
 * Loads the built-in theme documents (themes/*.yaml) in their canonical order.
 * The first theme is the default (it also gets the :root block).
 */
import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import type { ThemeDoc } from '../src/core.js';

export const ROOT = join(import.meta.dir, '..');
export const THEMES_DIR = join(ROOT, 'themes');
export const DIST_DIR = join(ROOT, 'dist');
export const PROTOTYPE = join(ROOT, '..', 'prototypes', 'm0-v2.html');

/** Canonical order: the default first, then as listed in 02-experience → Theming. */
export const THEME_ORDER = ['instrument-dark', 'instrument-light', 'oled', 'night', 'high-contrast', 'paper', 'neon'] as const;

export function readThemeFile(file: string): unknown {
  return Bun.YAML.parse(readFileSync(join(THEMES_DIR, file), 'utf8'));
}

/** Every theme document, ordered. Throws if a file is missing from THEME_ORDER or vice versa. */
export function loadThemes(): ThemeDoc[] {
  const files = readdirSync(THEMES_DIR).filter((f) => f.endsWith('.yaml')).sort();
  const ids = files.map((f) => f.replace(/\.yaml$/, ''));
  const missing = THEME_ORDER.filter((id) => !ids.includes(id));
  const extra = ids.filter((id) => !(THEME_ORDER as readonly string[]).includes(id));
  if (missing.length || extra.length) {
    throw new Error(`themes/ and THEME_ORDER disagree: missing ${missing.join(', ') || 'none'}, unlisted ${extra.join(', ') || 'none'}`);
  }
  return THEME_ORDER.map((id) => {
    const doc = readThemeFile(`${id}.yaml`) as ThemeDoc;
    if (doc.id !== id) throw new Error(`themes/${id}.yaml declares id "${doc.id}"`);
    return doc;
  });
}
