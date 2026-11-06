import { spawn } from 'node:child_process';
import { createWriteStream } from 'node:fs';
import { mkdir, mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

// Starts a hub on the Demo Home with a fresh data directory, as a first run.
// The binary is the release build, made beforehand from the repo root:
//
//   bun run build && go build -tags webui -o chadash ./cmd/chadash
//
// CHADASH_E2E_BIN points somewhere else.
export default async function globalSetup(): Promise<() => Promise<void>> {
  const port = Number(process.env.CHADASH_E2E_PORT ?? 3698);
  const bin = process.env.CHADASH_E2E_BIN ?? fileURLToPath(new URL('../../chadash', import.meta.url));
  const dataDir = await mkdtemp(join(tmpdir(), 'chadash-e2e-'));
  const hub = spawn(bin, ['serve', '--demo'], {
    env: { ...process.env, CHADASH_DATA_DIR: dataDir, CHADASH_LISTEN: `127.0.0.1:${port}` },
    stdio: ['ignore', 'pipe', 'pipe'],
  });

  // Everything the hub logs goes to e2e/.hub.log too, which CI keeps with the
  // report: a failed test comes with the hub's side of it.
  const logDir = fileURLToPath(new URL('.', import.meta.url));
  await mkdir(logDir, { recursive: true });
  const hubLog = createWriteStream(join(logDir, '.hub.log'));
  hub.stdout.pipe(hubLog, { end: false });
  hub.stderr.pipe(hubLog, { end: false });
  let output = '';
  await new Promise<void>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`no first-run note from ${bin} within 30 s:\n${output}`)), 30_000);
    const read = (chunk: Buffer) => {
      output += chunk.toString();
      if (output.includes('first run: no owner account yet')) {
        clearTimeout(timer);
        resolve();
      }
    };
    hub.stdout.on('data', read);
    hub.stderr.on('data', read);
    hub.on('error', (err) => reject(new Error(`can't start ${bin} (build it first, see this file): ${err.message}`)));
    hub.on('exit', (code) => reject(new Error(`${bin} exited (${code}) before it was ready:\n${output}`)));
  });

  const deadline = Date.now() + 30_000;
  for (;;) {
    try {
      if ((await fetch(`http://127.0.0.1:${port}/healthz`)).ok) break;
    } catch {
      // not listening yet
    }
    if (Date.now() > deadline) throw new Error(`the hub on :${port} never answered /healthz:\n${output}`);
    await new Promise((r) => setTimeout(r, 200));
  }

  return async () => {
    const exited = new Promise((r) => hub.once('exit', r));
    hub.kill('SIGTERM');
    await exited;
    hubLog.end();
    await rm(dataDir, { recursive: true, force: true });
  };
}
