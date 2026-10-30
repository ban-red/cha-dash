#!/usr/bin/env bun
/**
 * `bun run check`: what CI's Go and JavaScript jobs check, run here, with no build.
 *
 * gofmt, go vet (also with -tags devlogin, and for linux/386 and linux/arm/v7),
 * go fix, golangci-lint, govulncheck, the Dockerfile's COPY lines, home paths in
 * tests, tagged changelog sections, the typecheck, lint, the design tokens, then
 * the tests: Go with the race
 * detector (and the devlogin packages), and every workspace's. Everything runs
 * side by side, one step per core, the slow test suites first. What it leaves
 * to CI: the build, the images, the end-to-end tests (`bun run release:check`
 * runs those), the advisory TypeScript 7.1 typecheck (`bun run typecheck:next`),
 * the performance budgets and zizmor.
 *
 *   bun run check              the working tree as it is, other sessions' edits included
 *   bun run check --ref HEAD   a clean copy of a commit, in a temporary git worktree
 *   bun run check --quick      no race detector, no govulncheck: a minute or two faster
 *   bun run check --go         only the Go half (--js: only the JavaScript half)
 *
 * The pre-push hook (`bun run hooks` installs it) runs `--ref` on what you push.
 * Exits 1 if anything fails, with each failure's output.
 */
import { mkdtempSync, rmSync } from 'node:fs';
import { availableParallelism, tmpdir } from 'node:os';
import { dirname, join } from 'node:path';

const ROOT = dirname(import.meta.dir);
const argv = process.argv.slice(2);
const args = new Set(argv);
if (args.has('--help') || args.has('-h')) {
  const doc = (await Bun.file(import.meta.path).text()).match(/\/\*\*([\s\S]*?)\*\//)?.[1] ?? '';
  console.log(doc.replace(/^ \* ?/gm, '').trim());
  process.exit(0);
}
const QUICK = args.has('--quick');
const GO = !args.has('--js');
const JS = !args.has('--go');
const refAt = argv.indexOf('--ref');
const REF = refAt >= 0 ? argv[refAt + 1] : undefined;
if (refAt >= 0 && !REF) {
  console.error('check: --ref needs a commit, e.g. --ref HEAD');
  process.exit(2);
}

interface Step {
  name: string;
  cmd: string[];
  cwd?: string;
  env?: Record<string, string>;
  /** Fails the step when it prints anything (gofmt -l lists files and exits 0). */
  quietOk?: boolean;
}

/** Runs cmd and returns its exit code and output. */
async function run(cmd: string[], cwd: string, env: Record<string, string> = {}): Promise<{ code: number; out: string }> {
  const p = Bun.spawn(cmd, { cwd, env: { ...process.env, ...env }, stdout: 'pipe', stderr: 'pipe' });
  const [out, err, code] = await Promise.all([new Response(p.stdout).text(), new Response(p.stderr).text(), p.exited]);
  return { code, out: (out + err).trim() };
}

async function prepare(): Promise<{ dir: string; cleanup: () => Promise<void> }> {
  if (!REF) return { dir: ROOT, cleanup: async () => {} };
  const sha = await run(['git', 'rev-parse', '--verify', `${REF}^{commit}`], ROOT);
  if (sha.code !== 0) {
    console.error(`check: ${REF} isn't a commit`);
    process.exit(2);
  }
  const dir = mkdtempSync(join(tmpdir(), 'chadash-check-'));
  const cleanup = async () => {
    await run(['git', 'worktree', 'remove', '--force', dir], ROOT);
    rmSync(dir, { recursive: true, force: true });
  };
  const add = await run(['git', 'worktree', 'add', '--detach', dir, sha.out], ROOT);
  if (add.code !== 0) {
    console.error(add.out);
    await cleanup();
    process.exit(2);
  }
  console.log(`Checking ${sha.out.slice(0, 7)} in a clean worktree`);
  if (JS) {
    const install = await run(['bun', 'install', '--frozen-lockfile'], dir);
    if (install.code !== 0) {
      console.error(install.out);
      await cleanup();
      process.exit(1);
    }
  }
  return { dir, cleanup };
}

/** The steps, slowest first, so the pool starts the long test suites at once. */
function steps(dir: string): Step[] {
  const s: Step[] = [];
  const t: Step[] = [];
  if (GO) {
    s.push(
      { name: 'gofmt', cmd: ['sh', '-c', "git ls-files -z '*.go' | xargs -0 gofmt -l"], quietOk: true },
      { name: 'go vet', cmd: ['go', 'vet', './...'] },
      { name: 'go vet -tags devlogin', cmd: ['go', 'vet', '-tags', 'devlogin', './...'] },
      // CI runs the suite as 386, which only a Linux amd64 machine can do; vet catches what compiles differently.
      { name: 'go vet linux/386', cmd: ['go', 'vet', './...'], env: { GOOS: 'linux', GOARCH: '386' } },
      { name: 'go vet linux/arm/v7', cmd: ['go', 'vet', './...'], env: { GOOS: 'linux', GOARCH: 'arm', GOARM: '7' } },
      { name: 'go fix', cmd: ['go', 'fix', '-diff', './...'], quietOk: true },
      { name: 'golangci-lint', cmd: ['go', 'tool', '-modfile=tools/go.mod', 'golangci-lint', 'run'] },
      { name: 'Dockerfile COPY lines', cmd: ['python3', 'scripts/ci/dockerfile-copies.py'] },
      { name: 'home paths in tests', cmd: ['python3', 'scripts/ci/test-paths.py'] },
    );
    if (!QUICK) s.push({ name: 'govulncheck', cmd: ['go', 'tool', '-modfile=tools/go.mod', 'govulncheck', './...'] });
    const race = QUICK ? [] : ['-race'];
    t.push(
      { name: `go test${QUICK ? '' : ' -race'}`, cmd: ['go', 'test', ...race, './...'] },
      { name: 'go test -tags devlogin', cmd: ['go', 'test', ...race, '-tags', 'devlogin', './internal/auth/', './internal/server/', './cmd/chadash/'] },
    );
    if (process.platform === 'linux' && process.arch === 'x64') {
      t.push({ name: 'go test GOARCH=386', cmd: ['go', 'test', '-skip', '^TestBudget', './...'], env: { GOARCH: '386', CGO_ENABLED: '0' } });
    }
  }
  s.push({ name: 'released changelog sections', cmd: ['python3', 'scripts/ci/changelog-tagged.py'] });
  if (JS) {
    s.push(
      { name: 'typecheck', cmd: ['bun', 'run', 'typecheck'] },
      { name: 'lint', cmd: ['bun', 'run', 'lint'] },
      { name: 'design tokens', cmd: ['bun', 'run', 'generate', '--check'], cwd: join(dir, 'design/tokens') },
    );
    t.push(
      { name: 'bun run test', cmd: ['bun', 'run', 'test'] },
      { name: 'release scripts', cmd: ['bun', 'test', 'scripts/'] },
    );
  }
  return [...t, ...s];
}

interface Result {
  step: Step;
  ok: boolean;
  secs: number;
  out: string;
}

async function exec(step: Step, dir: string): Promise<Result> {
  const start = performance.now();
  const { code, out } = await run(step.cmd, step.cwd ?? dir, step.env);
  const ok = code === 0 && !(step.quietOk && out !== '');
  const r = { step, ok, secs: (performance.now() - start) / 1000, out };
  console.log(`${ok ? '✓' : '✗'} ${step.name} (${r.secs.toFixed(1)}s)`);
  return r;
}

/** Runs steps at most n at a time. */
async function pool(list: Step[], dir: string, n: number): Promise<Result[]> {
  const out: Result[] = [];
  let next = 0;
  await Promise.all(
    Array.from({ length: Math.min(n, list.length) }, async () => {
      while (next < list.length) out.push(await exec(list[next++]!, dir));
    }),
  );
  return out;
}

const started = performance.now();
const { dir, cleanup } = await prepare();
let results: Result[] = [];
try {
  results = await pool(steps(dir), dir, availableParallelism());
} finally {
  await cleanup();
}

const failed = results.filter((r) => !r.ok);
for (const r of failed) {
  const lines = r.out.split('\n');
  console.log(`\n── ${r.step.name} ${'─'.repeat(Math.max(0, 60 - r.step.name.length))}`);
  console.log((lines.length > 80 ? ['…', ...lines.slice(-80)] : lines).join('\n'));
}
const mins = ((performance.now() - started) / 60000).toFixed(1);
console.log(failed.length ? `\n${failed.length} of ${results.length} checks failed (${mins} min).` : `\nAll ${results.length} checks passed (${mins} min).`);
process.exit(failed.length ? 1 : 0);
