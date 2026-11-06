// bun run cutover:check [node]: plan 16's cutover, step 4. Read-only: it
// checks that every compose project on a Docker host is managed by Cha
// Dash and was last brought up from Cha Dash's file, and that every
// container runs healthy. It changes nothing, so it needs no go-ahead.
//
// It reads two things, both named in .env.local (Bun loads it):
//   DOCKER_<NODE>_URL           the host's Docker API (tcp:// or http(s)://), e.g. DOCKER_PVEDOCKER_URL
//   CHADASH_LAB_URL, CHADASH_LAB_TOKEN   the lab hub and an API token with graph.read
// The node defaults to pvedocker. Exit 0 when every check passes.
//
// Not checked here: each stack's plan saying "No changes" needs an admin
// session (POST …/validate). Drift (the file on disk against its commit)
// and the compose labels stand in for it.

export interface DockerContainer {
  Names: string[];
  State: string; // running, exited, …
  Status: string; // "Up 3 hours (healthy)"
  Labels: Record<string, string>;
}

export interface HubStack {
  name: string;
  status: string;
  path: string;
  file: string;
  drift: boolean;
  head: { short: string } | null;
  also_managed_by?: { manager: string }[];
}

export interface HubNode {
  node: string;
  name: string;
  online: boolean;
  stacks: HubStack[];
  detected: { name: string; reason?: string | null }[];
}

export interface Finding {
  ok: boolean;
  what: string;
}

const PROJECT = 'com.docker.compose.project';
const CONFIG = 'com.docker.compose.project.config_files';

/** The checks, from what Docker and the hub said. */
export function check(containers: DockerContainer[], node: HubNode | undefined): Finding[] {
  const out: Finding[] = [];
  const say = (ok: boolean, what: string) => out.push({ ok, what });
  const projects = new Map<string, DockerContainer[]>();
  for (const c of containers) {
    const p = c.Labels[PROJECT];
    const name = (c.Names[0] ?? '').replace(/^\//, '');
    if (!p) {
      say(false, `${name} isn't in a compose project, so no stack manages it`);
      continue;
    }
    projects.set(p, [...(projects.get(p) ?? []), c]);
  }
  if (!node) {
    say(false, 'the hub has no such node');
    return out;
  }
  if (!node.online) say(false, `the node ${node.name} isn't connected to the hub`);
  const managed = new Map(node.stacks.map((s) => [s.name, s]));
  for (const d of node.detected) say(false, `${d.name} runs but Cha Dash doesn't manage it${d.reason ? ` (${d.reason})` : ''}`);
  for (const [p, cs] of [...projects].sort(([a], [b]) => a.localeCompare(b))) {
    const s = managed.get(p);
    if (!s) {
      if (!node.detected.some((d) => d.name === p)) say(false, `${p} runs but the hub doesn't list it`);
      continue;
    }
    const want = `${s.path}/${s.file}`;
    const files = new Set(cs.map((c) => c.Labels[CONFIG] ?? ''));
    if (files.size !== 1 || !files.has(want)) {
      say(false, `${p} was last brought up from ${[...files].join(', ') || 'an unknown file'}, not Cha Dash's ${want}: another tool is its writer`);
    } else {
      say(true, `${p} was last brought up from ${want}`);
    }
    if (!s.head) say(false, `${p} has no commit yet`);
    if (s.drift) say(false, `${p}'s file was edited outside Cha Dash since its last commit`);
    for (const m of s.also_managed_by ?? []) say(false, `${m.manager} also lists a stack named ${p}`);
    for (const c of cs) {
      const name = (c.Names[0] ?? '').replace(/^\//, '');
      const unhealthy = /\((unhealthy|health: starting)\)/.test(c.Status);
      if (c.State !== 'running' || unhealthy) say(false, `${name} is ${c.State === 'running' ? c.Status : c.State}`);
    }
  }
  for (const s of node.stacks) if (!projects.has(s.name)) say(false, `${s.name} is managed but has no containers (status ${s.status})`);
  return out;
}

/** The Docker API's base URL from DOCKER_<NODE>_URL. */
export function dockerBase(raw: string): string {
  return raw.replace(/^tcp:\/\//, 'http://').replace(/\/+$/, '');
}

async function main(): Promise<number> {
  const node = (process.argv[2] ?? 'pvedocker').toLowerCase();
  const env = process.env;
  const docker = env[`DOCKER_${node.toUpperCase().replace(/-/g, '')}_URL`];
  const hub = env.CHADASH_LAB_URL;
  const token = env.CHADASH_LAB_TOKEN;
  const missing = [!docker && `DOCKER_${node.toUpperCase()}_URL`, !hub && 'CHADASH_LAB_URL', !token && 'CHADASH_LAB_TOKEN'].filter(Boolean);
  if (missing.length) {
    console.error(`Add ${missing.join(', ')} to .env.local (the token needs graph.read).`);
    return 2;
  }
  const res = await fetch(`${dockerBase(docker!)}/containers/json?all=1`);
  if (!res.ok) throw new Error(`Docker on ${node} answered ${res.status}`);
  const containers = (await res.json()) as DockerContainer[];
  const hr = await fetch(`${hub!.replace(/\/+$/, '')}/api/stacks`, { headers: { Authorization: `Bearer ${token}` } });
  if (!hr.ok) throw new Error(`the hub answered ${hr.status}`);
  const { nodes } = (await hr.json()) as { nodes: HubNode[] };
  const findings = check(containers, nodes.find((n) => n.name.toLowerCase() === node || n.node === node));
  for (const f of findings) console.log(`${f.ok ? 'ok  ' : 'FAIL'} ${f.what}`);
  const bad = findings.filter((f) => !f.ok).length;
  console.log(bad ? `\n${bad} check${bad === 1 ? '' : 's'} failed on ${node}.` : `\nEvery project on ${node} is Cha Dash's, and healthy.`);
  return bad ? 1 : 0;
}

if (import.meta.main) process.exit(await main());
