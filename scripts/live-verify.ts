// bun run live:verify [instance…]: layer 5 in docs/plan/17-test-automation.md.
// Runs the integrations once against the lab named in .env.local, read-only,
// and compares what the lab answered with the recorded fixtures. It fails
// when a recorded field is gone or has changed type: the fix is new
// fixtures and a layer 1–2 test. See internal/liveverify/README.md.
//
//   bun run live:verify                        # every instance in .env.local
//   bun run live:verify proxmox/pluto traefik  # some
//   bun run live:verify docker/jupiter -subscribe 0  # flags after the instances

const args = process.argv.slice(2);
// Instances first; from the first flag on, everything is the run's.
const first = args.findIndex((a) => a.startsWith('-'));
const only = first < 0 ? args : args.slice(0, first);
const flags = first < 0 ? [] : args.slice(first);
const cmd = ['go', 'test', '-tags', 'live', '-run', '^TestLive$', '-v', '-count=1', '-timeout', '20m', './internal/liveverify/', '-args', ...flags];
if (only.length > 0) cmd.push('-only', only.join(','));
const p = Bun.spawnSync(cmd, { stdio: ['inherit', 'inherit', 'inherit'], env: { ...process.env, CHADASH_LIVE: '1' } });
process.exit(p.exitCode ?? 1);
