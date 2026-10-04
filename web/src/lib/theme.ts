/**
 * The six built-in themes (design/tokens/themes/*.yaml) and their layout
 * defaults. Picking a theme sets `data-theme` on <html> plus the theme's
 * surface, density and Briefing style (tokens README, decision 12). The
 * choice is remembered per device; `public/boot.js` applies it before first
 * paint, so keep the two tables in step.
 */
import { ref } from 'vue';

export type ThemeId = 'instrument-dark' | 'instrument-light' | 'oled' | 'night' | 'high-contrast' | 'paper';

export interface ThemeInfo {
  id: ThemeId;
  name: string;
  note: string;
  surface: 'card' | 'ruled' | 'flat';
  density: 'comfortable' | 'compact';
  briefing: 'sentence' | 'headline';
}

export const THEMES: readonly ThemeInfo[] = [
  { id: 'instrument-dark', name: 'Instrument Dark', note: 'Warm graphite. The default', surface: 'card', density: 'comfortable', briefing: 'sentence' },
  { id: 'instrument-light', name: 'Instrument Light', note: 'Warm page, white cards', surface: 'card', density: 'comfortable', briefing: 'sentence' },
  { id: 'oled', name: 'OLED', note: 'True black for kiosks', surface: 'card', density: 'comfortable', briefing: 'sentence' },
  { id: 'night', name: 'Night', note: 'Red on black, for walls', surface: 'card', density: 'comfortable', briefing: 'sentence' },
  { id: 'high-contrast', name: 'High Contrast', note: 'Firm rules, brightest text', surface: 'card', density: 'comfortable', briefing: 'sentence' },
  { id: 'paper', name: 'Paper', note: 'Newsprint and headlines', surface: 'ruled', density: 'comfortable', briefing: 'headline' },
];

export const DEFAULT_THEME: ThemeId = 'instrument-dark';
export const STORAGE_KEY = 'chadash.theme';

export const isThemeId = (v: unknown): v is ThemeId => THEMES.some((t) => t.id === v);

/** The remembered theme, or the default. Storage can be missing or blocked. */
export function readStoredTheme(): ThemeId {
  try {
    const v = globalThis.localStorage?.getItem(STORAGE_KEY);
    return isThemeId(v) ? v : DEFAULT_THEME;
  } catch {
    return DEFAULT_THEME;
  }
}

export function storeTheme(id: ThemeId): void {
  try {
    globalThis.localStorage?.setItem(STORAGE_KEY, id);
  } catch {
    /* private mode or blocked storage: the choice lasts for this page only */
  }
}

export const currentTheme = ref<ThemeId>(DEFAULT_THEME);

export function applyTheme(id: ThemeId, root: HTMLElement = document.documentElement): void {
  const t = THEMES.find((x) => x.id === id) ?? THEMES[0]!;
  root.dataset['theme'] = t.id;
  root.dataset['surface'] = t.surface;
  root.dataset['density'] = t.density;
  root.dataset['briefing'] = t.briefing;
  currentTheme.value = t.id;
  // theme-color follows the page background (any CSS colour works there).
  const meta = document.querySelector('meta[name="theme-color"]');
  if (meta && typeof getComputedStyle === 'function') {
    const bg = getComputedStyle(root).getPropertyValue('--bg').trim();
    if (bg) meta.setAttribute('content', bg);
  }
}

export function setTheme(id: ThemeId): void {
  applyTheme(id);
  storeTheme(id);
}
