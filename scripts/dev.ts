#!/usr/bin/env bun
/**
 * `bun run dev`: a local Cha Dash for working on the frontend.
 *
 * Builds and starts the hub on the Demo Home simulator (demo mode, data in ./data,
 * on 127.0.0.1:3696), waits until it's healthy, then starts the Vite dev server with
 * hot reload, which proxies /api and /ws to the hub. Ctrl-C stops both.
 *
 * The hub is built with -tags devlogin, so the setup and sign-in screens offer
 * "Login as Local Dev": one click, no setup token, no password. It signs in as the
 * owner, creating a passwordless `dev` owner on first run. Release builds don't
 * contain it. The setup token is still printed if you'd rather go through setup.
 *
 *   bun run dev              reuse ./data, so your owner account and layouts survive
 *   bun run dev --fresh      wipe ./data first: a brand-new instance and setup token
 *   bun run dev --now        run the demo on the wall clock instead of from 14:05
 *   bun run dev --check      check prerequisites and ports, start nothing
 *
 * CHADASH_* variables pass through to the hub, e.g. `CHADASH_DEMO_SEED=2 bun run dev`.
 * If a hub already answers on :3696, it's reused and only the dev server starts.
 */
import { existsSync, rmSync } from 'node:fs';
import { dirname, join, relative } from 'node:path';

const ROOT = dirname(import.meta.dir);
const args = new Set(process.argv.slice(2));
if (args.has('--help') || args.has('-h')) {
  const doc = (await Bun.file(import.meta.path).text()).match(/\/\*\*([\s\S]*?)\*\//)?.[1] ?? '';
  console.log(doc.replace(/^ \* ?/gm, '').trim());
  process.exit(0);
}

const LISTEN = process.env.CHADASH_LISTEN ?? '127.0.0.1:3696';
const HUB = `http://${LISTEN.startsWith(':') ? '127.0.0.1' + LISTEN : LISTEN}`;
const DATA = process.env.CHADASH_DATA_DIR ?? join(ROOT, 'data');
const BIN = join(ROOT, 'bin', 'chadash-dev');
const WEB_PORT = 3697; // matches web/vite.config.ts
const WEB_URL = `http://localhost:${WEB_PORT}`;

const tty = process.stdout.isTTY;
const paint = (code: string) => (s: string) => (tty ? `\x1b[${code}m${s}\x1b[0m` : s);
const dim = paint('2'), bold = paint('1'), cyan = paint('36'), magenta = paint('35'), yellow = paint('33'), red = paint('31');
const say = (s: string) => console.log(`${bold('▸')} ${s}`);
const fail = (s: string): never => {
  console.error(`${red('■')} ${s}`);
  process.exit(1);
};

async function healthy(): Promise<boolean> {
  try {
    const r = await fetch(`${HUB}/healthz`, { signal: AbortSignal.timeout(800) });
    return r.ok;
  } catch {
    return false;
  }
}

async function portFree(port: number): Promise<boolean> {
  try {
    const server = Bun.listen({ hostname: '127.0.0.1', port, socket: { data() {} } });
    server.stop(true);
    return true;
  } catch {
    return false;
  }
}

// ── Preflight ────────────────────────────────────────────────────────────────
if (!Bun.which('go')) fail('Go is not installed. go.mod pins Go 1.27.3; any Go from 1.21 on fetches it.');
if (!existsSync(join(ROOT, 'node_modules'))) {
  say('Installing JavaScript dependencies (bun install)');
  const install = Bun.spawnSync(['bun', 'install', '--frozen-lockfile'], { cwd: ROOT, stdout: 'inherit', stderr: 'inherit' });
  if (install.exitCode !== 0) fail('bun install failed');
}
const reuseHub = await healthy();
if (!reuseHub && !(await portFree(Number(LISTEN.split(':').pop())))) {
  fail(`Something that isn't a Cha Dash hub is using ${LISTEN}. Stop it, or set CHADASH_LISTEN.`);
}
if (!(await portFree(WEB_PORT))) fail(`Port ${WEB_PORT} is in use (another Vite dev server?). Stop it first.`);

if (args.has('--check')) {
  say(`Go ${dim(Bun.spawnSync(['go', 'env', 'GOVERSION'], { cwd: ROOT }).stdout.toString().trim())} · Bun ${dim(Bun.version)} · dependencies installed`);
  say(reuseHub ? `A hub is already running at ${HUB}; it would be reused` : `${LISTEN} and :${WEB_PORT} are free`);
  say(`Data directory: ${relative(ROOT, DATA) || DATA}${existsSync(DATA) ? '' : dim(' (created on first run)')}`);
  process.exit(0);
}

// ── Hub ──────────────────────────────────────────────────────────────────────
const children: Bun.Subprocess[] = [];
let stopping = false;

function stopAll(code = 0) {
  if (stopping) return;
  stopping = true;
  for (const c of children) if (c.exitCode === null) c.kill('SIGINT');
  setTimeout(() => {
    for (const c of children) if (c.exitCode === null) c.kill('SIGKILL');
    process.exit(code);
  }, 4000).unref();
  Promise.all(children.map((c) => c.exited)).then(() => process.exit(code));
}
process.on('SIGINT', () => stopAll(0));
process.on('SIGTERM', () => stopAll(0));

/** Prefix each line of a child's output, and watch it for the setup token. */
async function pipe(stream: ReadableStream<Uint8Array>, label: string, onLine?: (line: string) => void) {
  const decoder = new TextDecoder();
  let rest = '';
  for await (const chunk of stream) {
    const lines = (rest + decoder.decode(chunk, { stream: true })).split('\n');
    rest = lines.pop() ?? '';
    for (const line of lines) {
      onLine?.(line);
      console.log(`${label} ${line}`);
    }
  }
  if (rest) console.log(`${label} ${rest}`);
}

if (!reuseHub) {
  if (args.has('--fresh')) {
    if (process.env.CHADASH_DATA_DIR) fail('--fresh only wipes the default ./data. Unset CHADASH_DATA_DIR, or delete your directory yourself.');
    if (existsSync(DATA)) {
      rmSync(DATA, { recursive: true, force: true });
      say('Wiped ./data: this is a brand-new instance');
    }
  }

  say('Building the hub');
  // devlogin: the one-click "Login as Local Dev" (internal/auth/devlogin.go). Never in release builds.
  const build = Bun.spawnSync(['go', 'build', '-tags', 'devlogin', '-o', BIN, './cmd/chadash'], { cwd: ROOT, stdout: 'inherit', stderr: 'inherit' });
  if (build.exitCode !== 0) fail('go build failed');

  const env: Record<string, string> = {
    ...(process.env as Record<string, string>),
    CHADASH_DATA_DIR: DATA,
    CHADASH_LISTEN: LISTEN,
    CHADASH_LOG_FORMAT: process.env.CHADASH_LOG_FORMAT ?? 'text',
  };
  if (args.has('--now')) env.CHADASH_DEMO_AT = 'now';

  say(`Starting the hub on the Demo Home at ${HUB}`);
  const hub = Bun.spawn([BIN, 'serve', '--demo'], { cwd: ROOT, env, stdout: 'pipe', stderr: 'pipe' });
  children.push(hub);
  let token = '';
  const watch = (line: string) => {
    const m = line.match(/setup_token=(cds_[a-z0-9]+)/);
    if (m && m[1]) token = m[1];
  };
  pipe(hub.stdout, cyan('hub │'), watch);
  pipe(hub.stderr, cyan('hub │'), watch);
  hub.exited.then((code) => {
    if (!stopping) {
      console.error(`${red('■')} The hub exited (code ${code}).`);
      stopAll(code || 1);
    }
  });

  const deadline = Date.now() + 60_000;
  while (!(await healthy())) {
    if (hub.exitCode !== null) fail('The hub stopped before it became healthy; see its log above.');
    if (Date.now() > deadline) {
      stopAll(1);
      fail('The hub did not become healthy within 60 s.');
    }
    await Bun.sleep(250);
  }
  // The token is logged during startup, before /healthz answers.
  await Bun.sleep(100);
  if (token) {
    console.log(`\n  ${yellow('First run.')} Open ${bold(WEB_URL)} and press ${bold('Login as Local Dev')}, or set up an owner with this token:\n\n      ${bold(token)}\n`);
  } else {
    console.log(`\n  Open ${bold(WEB_URL)} and press ${bold('Login as Local Dev')}${dim(' (signs in as the owner). Start over with')} bun run dev --fresh\n`);
  }
} else {
  say(`Reusing the hub already running at ${HUB} ${dim('(Login as Local Dev shows only if it was built by bun run dev)')}`);
}

// ── Web ──────────────────────────────────────────────────────────────────────
say(`Starting the web dev server at ${WEB_URL} (hot reload; /api and /ws go to the hub)`);
const web = Bun.spawn(['bun', 'run', 'dev'], { cwd: join(ROOT, 'web'), stdout: 'pipe', stderr: 'pipe' });
children.push(web);
pipe(web.stdout, magenta('web │'));
pipe(web.stderr, magenta('web │'));
web.exited.then((code) => {
  if (!stopping) {
    console.error(`${red('■')} The web dev server exited (code ${code}).`);
    stopAll(code || 1);
  }
});
