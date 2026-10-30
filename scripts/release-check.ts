#!/usr/bin/env bun
/**
 * `bun run release:check [version]`: everything CI would say about a release
 * commit, before you tag it. On a clean copy of the commit (a temporary git
 * worktree, so nobody's uncommitted edits count) it runs `bun run check`,
 * builds the release binary with the SPA embedded as CI's End-to-end job does,
 * and runs the end-to-end tests: the first run, the front page on desktop and
 * phone, axe in every theme, the command bar and the action engine. With a
 * version it also checks that version's release notes (scripts/release-notes.sh).
 *
 *   bun run release:check 0.2.0-beta.3                  HEAD, and its notes
 *   bun run release:check 0.2.0-beta.3 --ref <commit>   another commit
 *
 * It builds, so run it yourself (agents only when told to). It needs Node.js for
 * Playwright's test runner, and installs Playwright's Chromium the first time.
 * Takes about ten minutes; stops at the first step that fails.
 */
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';

const ROOT = dirname(import.meta.dir);
const argv = process.argv.slice(2);
if (argv.includes('--help') || argv.includes('-h')) {
  const doc = (await Bun.file(import.meta.path).text()).match(/\/\*\*([\s\S]*?)\*\//)?.[1] ?? '';
  console.log(doc.replace(/^ \* ?/gm, '').trim());
  process.exit(0);
}
const refAt = argv.indexOf('--ref');
const ref = refAt >= 0 ? argv[refAt + 1] : 'HEAD';
const version = argv.find((a, i) => !a.startsWith('--') && (refAt < 0 || i !== refAt + 1));

function sh(cmd: string[], cwd: string, env: Record<string, string> = {}): number {
  return Bun.spawnSync(cmd, { cwd, env: { ...process.env, ...env }, stdout: 'inherit', stderr: 'inherit' }).exitCode;
}

if (Bun.spawnSync(['node', '--version']).exitCode !== 0) {
  console.error('release:check: Playwright needs Node.js (any current LTS) on the PATH');
  process.exit(2);
}
const rev = Bun.spawnSync(['git', 'rev-parse', '--verify', `${ref}^{commit}`], { cwd: ROOT });
if (rev.exitCode !== 0) {
  console.error(`release:check: ${ref} isn't a commit`);
  process.exit(2);
}
const sha = rev.stdout.toString().trim();
const dir = mkdtempSync(join(tmpdir(), 'chadash-release-'));
const steps: [string, string[], string, Record<string, string>?][] = [
  ['Worktree', ['git', 'worktree', 'add', '--detach', dir, sha], ROOT],
  ...(version ? ([['Release notes', ['sh', 'scripts/release-notes.sh', version], dir]] as [string, string[], string][]) : []),
  ['bun install', ['bun', 'install', '--frozen-lockfile'], dir],
  ['bun run check', ['bun', 'scripts/check.ts'], dir],
  ['Build the SPA', ['bun', 'run', 'build'], dir],
  ['Build the hub with the SPA embedded', ['go', 'build', '-tags', 'webui', '-trimpath', '-o', 'chadash', './cmd/chadash'], dir, { CGO_ENABLED: '0' }],
  ['Playwright Chromium', ['bunx', 'playwright', 'install', 'chromium'], join(dir, 'web')],
  ['End-to-end tests', ['bun', 'run', 'e2e'], dir],
];

let failed = '';
const started = performance.now();
for (const [name, cmd, cwd, env] of steps) {
  console.log(`\n▶ ${name}`);
  if (sh(cmd, cwd, env) !== 0) {
    failed = name;
    break;
  }
}
Bun.spawnSync(['git', 'worktree', 'remove', '--force', dir], { cwd: ROOT });
rmSync(dir, { recursive: true, force: true });
const mins = ((performance.now() - started) / 60000).toFixed(1);
if (failed) {
  console.error(`\n✗ ${failed} failed on ${sha.slice(0, 7)} (${mins} min). Fix it before tagging.`);
  process.exit(1);
}
console.log(`\n✓ ${sha.slice(0, 7)} is ready to tag${version ? ` as v${version.replace(/^v/, '')}` : ''} (${mins} min).`);
