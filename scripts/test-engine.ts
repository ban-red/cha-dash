// bun run test:engine [packages…]: the engine tests (layer 3 in
// docs/plan/17-test-automation.md) against the Docker Engine the
// environment names (DOCKER_HOST, or the default socket), then the leak
// check, which runs even when a test failed.
//
// Engine tests are the `engine` build tag's TestEngine* tests. They start
// only throwaway, labelled containers, networks, volumes and a registry, and
// never build an image. Without an engine they skip; CI sets
// CHADASH_REQUIRE_ENGINE=1 so they fail instead.
//
//   bun run test:engine                          # every package with engine tests
//   bun run test:engine ./internal/node/stacks/  # one package
//   bun run test:engine -- -run TestEngineCanaries ./internal/testengine/

const args = process.argv.slice(2);
const run = args.includes('-run') ? [] : ['-run', '^TestEngine'];
// Without packages named: every package with an engine-tagged test file.
// Other packages' tests that happen to start with TestEngine stay out.
function enginePackages(): string[] {
  const files = Bun.spawnSync(['git', 'grep', '-l', '--untracked', '--', '^//go:build engine$', '*_test.go']).stdout.toString().trim().split('\n');
  return [...new Set(files.filter(Boolean).map((f) => './' + f.slice(0, f.lastIndexOf('/') + 1)))].sort();
}
const pkgs = args.some((a) => a.startsWith('./')) ? [] : enginePackages();
if (!args.some((a) => a.startsWith('./')) && pkgs.length === 0) {
  console.error('test:engine: no package has a //go:build engine test file');
  process.exit(1);
}

function go(cmd: string[], env: Record<string, string> = {}): number {
  console.log(`$ go ${cmd.join(' ')}`);
  const p = Bun.spawnSync(['go', ...cmd], { stdio: ['inherit', 'inherit', 'inherit'], env: { ...process.env, ...env } });
  return p.exitCode ?? 1;
}

const tests = go(['test', '-tags', 'engine', '-count=1', '-timeout', '30m', ...run, ...args, ...pkgs]);
const leaks = go(['test', '-tags', 'engine', '-count=1', '-run', '^TestNoLeaks$', './internal/testengine/'], { CHADASH_ENGINE_LEAKCHECK: '1' });
process.exit(tests || leaks);
