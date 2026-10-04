/**
 * Print the validator and contrast results as Markdown tables (for README.md),
 * plus the exact dataviz validator commands that reproduce the status-pair runs.
 *
 *   bun run report
 */
import { apca, checkTheme, moreContrast, resolveTheme, separation, TEXT_PAIRS, wcag } from '../src/core.js';
import { loadThemes } from './themes.ts';

const f1 = (n: number) => n.toFixed(1);
const f2 = (n: number) => n.toFixed(2);
const themes = loadThemes().map((d) => resolveTheme(d));

console.log('### Status pairs\n');
console.log('| Theme | Attention | Down | Protan ΔE | Deutan ΔE | Tritan ΔE | Normal ΔE | Attention on bg / surface | Down on bg / surface | Accent vs attention / down |');
console.log('|---|---|---|---|---|---|---|---|---|---|');
for (const t of themes) {
  const a = t.hex['--attention'], d = t.hex['--down'], acc = t.hex['--accent'];
  const s = separation(a, d);
  const mono = t.monochrome ? ' (monochrome: glyph only)' : '';
  console.log(`| ${t.name}${mono} | \`${t.tokens['--attention']}\` ${a} | \`${t.tokens['--down']}\` ${d} | ${f1(s.protan)} | ${f1(s.deutan)} | ${f1(s.tritan)} | ${f1(s.normal)} | ${f2(wcag(a, t.hex['--bg']))} / ${f2(wcag(a, t.hex['--surface']))} | ${f2(wcag(d, t.hex['--bg']))} / ${f2(wcag(d, t.hex['--surface']))} | ${f1(separation(acc, a).normal)} / ${f1(separation(acc, d).normal)} |`);
}

console.log('\n### Text contrast (APCA Lc / WCAG ratio)\n');
console.log('| Theme | Profile | text on bg | text on surface | text-2 on bg | text-2 on surface | text-3 on bg | text-3 on surface |');
console.log('|---|---|---|---|---|---|---|---|');
for (const t of themes) {
  const cell = (tok: string, s: string) => `${f1(Math.abs(apca(t.hex[tok], t.hex[s])))} / ${f2(wcag(t.hex[tok], t.hex[s]))}`;
  console.log(`| ${t.name} | ${t.profile} | ${cell('--text', '--bg')} | ${cell('--text', '--surface')} | ${cell('--text-2', '--bg')} | ${cell('--text-2', '--surface')} | ${cell('--text-3', '--bg')} | ${cell('--text-3', '--surface')} |`);
}

console.log('\n### Text/background pairs (WCAG 2 AA, 4.5:1)\n');
console.log(`Every pair in \`TEXT_PAIRS\` (${TEXT_PAIRS.length}), in each theme and its \`prefers-contrast: more\` variant.\n`);
console.log('| Theme | Weakest pair | Ratio | Weakest, more contrast | Ratio |');
console.log('|---|---|---|---|---|');
const weakest = (t: ReturnType<typeof resolveTheme>) =>
  TEXT_PAIRS.map(([fg, bg]) => ({ pair: `${fg} on ${bg}`, r: wcag(t.hex[`--${fg}`], t.hex[`--${bg}`]) })).sort((a, b) => a.r - b.r)[0]!;
for (const t of themes) {
  const a = weakest(t), b = weakest(resolveTheme(moreContrast(t.doc)));
  console.log(`| ${t.name} | ${a.pair} | ${f2(a.r)} | ${b.pair} | ${f2(b.r)} |`);
}

console.log('\n### Check summary\n');
for (const t of themes) {
  const c = checkTheme(t);
  const warn = c.checks.filter((x) => !x.pass && x.severity !== 'error');
  console.log(`- **${t.name}**: ${c.ok ? 'passes' : 'REFUSED: ' + c.refusal} (${c.checks.length} checks${warn.length ? `, ${warn.length} warnings: ${warn.map((w) => w.id).join(', ')}` : ''})${t.notes.length ? `. Notes: ${t.notes.join('; ')}` : ''}`);
}

console.log('\n### Dataviz validator commands\n');
console.log('```sh');
for (const t of themes.filter((x) => !x.monochrome)) {
  console.log(`node validate_palette.js "${t.hex['--attention']},${t.hex['--down']}" --mode ${t.scheme} --surface "${t.hex['--surface']}"   # ${t.name}`);
}
console.log('```');
