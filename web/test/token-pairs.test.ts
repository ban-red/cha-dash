/**
 * Every rule in the web app's CSS that sets both a text colour and a
 * background from tokens (`color: var(--a)` with `background: var(--b)`)
 * must meet WCAG AA, 4.5:1, in all six themes, as the generated tokens have
 * them. design/tokens checks every token pair it defines; this checks the
 * pairs the components actually make, which axe can't see in happy-dom.
 */
import { readdirSync, readFileSync } from 'node:fs';
import { join, relative } from 'node:path';
import { describe, expect, it } from 'vitest';
import tokensRaw from '@chadash/tokens/tokens.json?raw';

// Read from disk: Vitest doesn't process CSS, so a ?raw import of a .css file is empty.
const SRC = join(import.meta.dirname, '..', 'src');
const walk = (dir: string): string[] => readdirSync(dir, { withFileTypes: true }).flatMap((e) => (e.isDirectory() ? walk(join(dir, e.name)) : [join(dir, e.name)]));
const sources = Object.fromEntries(walk(SRC).filter((f) => /\.(css|vue)$/.test(f)).map((f) => [relative(SRC, f), readFileSync(f, 'utf8')]));
const themes = (JSON.parse(tokensRaw) as { themes: Record<string, { hex: Record<string, string> }> }).themes;

function luminance(hex: string): number {
  const [r, g, b] = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255).map((c) => (c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4));
  return 0.2126 * r! + 0.7152 * g! + 0.0722 * b!;
}
const ratio = (a: string, b: string) => {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi! + 0.05) / (lo! + 0.05);
};

/** [file, selector, foreground token, background token] for every rule that sets both. */
function declaredPairs(): [string, string, string, string][] {
  const out: [string, string, string, string][] = [];
  for (const [file, src] of Object.entries(sources)) {
    const css = (file.endsWith('.vue') ? [...src.matchAll(/<style[^>]*>([\s\S]*?)<\/style>/g)].map((m) => m[1]).join('\n') : src).replace(/\/\*[\s\S]*?\*\//g, '');
    for (const m of css.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
      const decls = m[2]!;
      const fg = /(?:^|;)\s*color:\s*var\((--[\w-]+)\)\s*(?:;|$)/.exec(decls)?.[1];
      const bg = /(?:^|;)\s*background(?:-color)?:\s*var\((--[\w-]+)\)\s*(?:;|$)/.exec(decls)?.[1];
      if (fg && bg) out.push([file, m[1]!.trim().replace(/\s+/g, ' '), fg, bg]);
    }
  }
  return out;
}

describe('text/background pairs the components declare', () => {
  const pairs = declaredPairs();

  it('finds them (the scan sees the CSS)', () => {
    expect(Object.keys(sources).length).toBeGreaterThan(20);
    // .btn.primary, the Inbox badge, solid danger buttons, status monograms…
    expect(pairs.length).toBeGreaterThanOrEqual(5);
    expect(pairs.some(([, sel, fg, bg]) => sel.includes('.btn.primary') && fg === '--text-inverse' && bg === '--text')).toBe(true);
  });

  it('meet WCAG AA in every theme', () => {
    const failing: string[] = [];
    for (const [id, t] of Object.entries(themes)) {
      for (const [file, sel, fg, bg] of pairs) {
        const a = t.hex[fg], b = t.hex[bg];
        if (!a || !b) continue; // translucent tokens have no single hex
        const r = ratio(a, b);
        if (r < 4.5) failing.push(`${id}: ${file} ${sel}: ${fg} on ${bg} is ${r.toFixed(2)}:1`);
      }
    }
    expect(failing).toEqual([]);
  });
});
