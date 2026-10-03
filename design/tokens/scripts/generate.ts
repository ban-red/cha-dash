/**
 * Generate dist/tokens.css and dist/tokens.json from themes/*.yaml.
 *
 *   bun run generate          write dist/
 *   bun run generate --check  exit 1 if dist/ is stale (CI)
 *
 * Refuses to write anything if a theme document is invalid or fails an
 * error-level accessibility check, printing the specific reason.
 */
import { mkdirSync, readFileSync, writeFileSync, existsSync } from 'node:fs';
import { join } from 'node:path';
import { buildCss, buildJson, checkTheme, resolveTheme, validateThemeDoc } from '../src/core.js';
import { DIST_DIR, loadThemes } from './themes.ts';

export function generate() {
  const docs = loadThemes();
  const problems: string[] = [];
  for (const doc of docs) {
    const errs = validateThemeDoc(doc);
    if (errs.length) {
      problems.push(...errs.map((e) => `themes/${doc.id}.yaml: ${e.path || '(root)'} ${e.message}`));
      continue;
    }
    const result = checkTheme(resolveTheme(doc));
    if (!result.ok) problems.push(`themes/${doc.id}.yaml: refused: ${result.refusal}`);
  }
  if (problems.length) throw new Error(problems.join('\n'));
  return {
    css: buildCss(docs),
    json: JSON.stringify(buildJson(docs), null, 2) + '\n',
  };
}

if (import.meta.main) {
  const check = process.argv.includes('--check');
  let out: { css: string; json: string };
  try {
    out = generate();
  } catch (e) {
    console.error((e as Error).message);
    process.exit(1);
  }
  const files = { 'tokens.css': out.css, 'tokens.json': out.json };
  if (check) {
    const stale = Object.entries(files).filter(([f, s]) => !existsSync(join(DIST_DIR, f)) || readFileSync(join(DIST_DIR, f), 'utf8') !== s);
    if (stale.length) {
      console.error(`dist/ is stale: ${stale.map(([f]) => f).join(', ')}. Run \`bun run generate\`.`);
      process.exit(1);
    }
    console.log('dist/ is up to date');
  } else {
    mkdirSync(DIST_DIR, { recursive: true });
    for (const [f, s] of Object.entries(files)) writeFileSync(join(DIST_DIR, f), s);
    console.log(`wrote dist/tokens.css (${(out.css.length / 1024).toFixed(1)} KB) and dist/tokens.json (${(out.json.length / 1024).toFixed(1)} KB)`);
  }
}
