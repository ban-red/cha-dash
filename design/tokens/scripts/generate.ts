/**
 * Generate dist/tokens.css and dist/tokens.json from themes/*.yaml.
 *
 *   bun run generate          write dist/
 *   bun run generate --check  exit 1 if dist/ is stale (CI)
 *
 * Refuses to write anything if a theme document is invalid or fails an
 * error-level accessibility check, printing the specific reason.
 */
import { existsSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { buildCssWithAssets, buildJson, checkTheme, resolveTheme, validateThemeDoc } from '../src/core.js';
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
  const { css, assets } = buildCssWithAssets(docs);
  return {
    css,
    json: JSON.stringify(buildJson(docs), null, 2) + '\n',
    assets,
  };
}

if (import.meta.main) {
  const check = process.argv.includes('--check');
  let out: ReturnType<typeof generate>;
  try {
    out = generate();
  } catch (e) {
    console.error((e as Error).message);
    process.exit(1);
  }
  const files: Record<string, string> = { 'tokens.css': out.css, 'tokens.json': out.json, ...out.assets };
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
    for (const f of readdirSync(DIST_DIR)) if (/^grain-[0-9a-f]{8}\.svg$/.test(f) && !(f in files)) rmSync(join(DIST_DIR, f));
    console.log(`wrote dist/tokens.css (${(out.css.length / 1024).toFixed(1)} KB), dist/tokens.json (${(out.json.length / 1024).toFixed(1)} KB) and ${Object.keys(out.assets).length} asset(s)`);
  }
}
