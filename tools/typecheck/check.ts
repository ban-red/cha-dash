// Runs TypeScript 7.1 with Vue's content mapper on each project and fails
// on any error, except the mapper's known false TS2367 ("This comparison
// appears to be unintentional") inside a .vue file's <template>:
// vuejs/language-tools#6306, an event handler in a v-else-if branch.
// vue-tsc, the gate, still checks those lines. Every other error, and a
// TS2367 in a script, fails.

import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const projects = ['web.json', 'layout-vue.json'];
const known = 'vuejs/language-tools#6306';

// templateLines is each .vue file's <template> span, 1-based and inclusive.
const spans = new Map<string, [number, number] | null>();
function inTemplate(file: string, line: number): boolean {
  if (!file.endsWith('.vue')) return false;
  if (!spans.has(file)) {
    const lines = readFileSync(file, 'utf8').split('\n');
    const start = lines.findIndex((l) => l.startsWith('<template'));
    const end = lines.findLastIndex((l) => l.startsWith('</template>'));
    spans.set(file, start >= 0 && end > start ? [start + 1, end + 1] : null);
  }
  const s = spans.get(file);
  return !!s && line >= s[0] && line <= s[1];
}

const errorLine = /^(.+?)\((\d+),(\d+)\): error (TS\d+): /;
let failed = false;
let skipped = 0;
for (const p of projects) {
  const run = Bun.spawnSync(['tsc', '--runExternalCode', '-p', p], { cwd: import.meta.dir, stdout: 'pipe', stderr: 'pipe' });
  const out = `${run.stdout.toString()}${run.stderr.toString()}`;
  const seen = new Set<string>();
  let errors = 0;
  let matched = false;
  for (const line of out.split('\n')) {
    const m = errorLine.exec(line);
    if (!m) {
      if (line.trim() && run.exitCode !== 0 && !line.startsWith(' ')) console.log(line);
      continue;
    }
    matched = true;
    const [, rel, ln, , code] = m;
    if (code === 'TS2367' && inTemplate(resolve(import.meta.dir, rel), Number(ln))) {
      skipped++;
      continue;
    }
    if (!seen.has(line)) {
      seen.add(line);
      console.log(line);
      errors++;
    }
  }
  // A failure with no error line (a crash, a bad config) fails too.
  if (errors > 0 || (run.exitCode !== 0 && !matched)) failed = true;
}
console.log(`${skipped} TS2367 in templates skipped (${known}).`);
process.exit(failed ? 1 : 0);
