/**
 * Prints the scenario results as Markdown tables (the README's behaviour
 * table comes from here). Run: `bun run report`; VERBOSE=1 adds every layout.
 */
import './harness/dom.ts';
import { layoutKey } from './harness/checks.ts';
import { ENGINES } from './harness/engines.ts';
import { problemsOf, runAll } from './harness/run.ts';

const all = ENGINES.map((e) => ({ e, results: runAll(e.make) }));
const ours = all[0]!.results;

console.log(`| Scenario | ${ENGINES.map((e) => e.label).join(' | ')} |`);
console.log(`|---|${ENGINES.map(() => '---').join('|')}|`);
for (const [i, r] of ours.entries()) {
  const cells = all.map(({ results }) => {
    const x = results[i]!;
    const p = problemsOf(x);
    const parts: string[] = [];
    if (p.overlaps || p.outOfBounds || p.overflow) parts.push(`**${p.overlaps} overlaps, ${p.outOfBounds} out of bounds, ${p.overflow} overflowing**`);
    if (p.offSystem) parts.push(`${p.offSystem} off-system sizes`);
    else if (p.unsupported) parts.push(`${p.unsupported} undeclared sizes`);
    const last = x.steps.at(-1)!;
    if (x.steps.length > 1) parts.push(`${last.problems.gaps} empty cells, ${last.moved.length} moved`);
    const same = x.steps.every((s, k) => layoutKey(s.snapshot) === layoutKey(ours[i]!.steps[k]!.snapshot));
    if (x !== ours[i] && x.steps.length > 1) parts.push(same ? 'same as ours' : 'differs from ours');
    for (const [k, v] of Object.entries(x.findings)) if (k !== 'bytes' && k !== 'noOps') parts.push(`${k}: ${v === true ? 'yes' : v === false ? '**no**' : v}`);
    return parts.join('; ') || 'ok';
  });
  console.log(`| ${r.scenario.title} | ${cells.join(' | ')} |`);
}

if (process.env.VERBOSE) {
  for (const { e, results } of all) {
    console.log(`\n### ${e.label}`);
    for (const r of results) for (const s of r.steps) console.log(`- ${r.scenario.id} · ${s.label}: ${layoutKey(s.snapshot)}`);
  }
}
