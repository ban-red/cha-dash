/**
 * Runtime dependency trees and licences of each option, from the installed
 * packages (Vue itself excluded: every option needs it). Run: `bun run deps`.
 */
import { readFileSync, realpathSync } from 'node:fs';
import { dirname, join } from 'node:path';

const ROOT = join(import.meta.dir, '..');

interface Pkg {
  name: string;
  version: string;
  license?: string;
  dependencies?: Record<string, string>;
}

function resolveDir(name: string, from: string): string {
  for (let d = from; ; d = dirname(d)) {
    try {
      return realpathSync(join(d, 'node_modules', name));
    } catch {
      if (dirname(d) === d) throw new Error(`can't resolve ${name} from ${from}`);
    }
  }
}

function readPkg(dir: string): Pkg {
  return JSON.parse(readFileSync(join(dir, 'package.json'), 'utf8')) as Pkg;
}

/** Every package reachable through `dependencies`, as name@version → licence. */
export function tree(dir: string, seen = new Map<string, string>()): Map<string, string> {
  const pkg = readPkg(dir);
  for (const dep of Object.keys(pkg.dependencies ?? {})) {
    if (dep === 'vue' || dep.startsWith('@chadash/')) continue;
    const d = resolveDir(dep, dir);
    const p = readPkg(d);
    const key = `${p.name}@${p.version}`;
    if (seen.has(key)) continue;
    seen.set(key, p.license ?? 'unknown');
    tree(d, seen);
  }
  return seen;
}

export const OPTIONS = [
  { label: '@chadash/layout', dir: join(ROOT, '../../packages/layout') },
  { label: '@chadash/layout-vue (renderer)', dir: join(ROOT, '../../packages/layout-vue') },
  { label: 'gridstack 14', dir: resolveDir('gridstack', ROOT) },
  { label: '@dnd-kit/dom 0.5', dir: resolveDir('@dnd-kit/dom', ROOT) },
];

if (import.meta.main) {
  console.log('| Package | Own licence | Runtime deps (transitive, excl. Vue) | Their licences |');
  console.log('|---|---|---:|---|');
  for (const o of OPTIONS) {
    const own = readPkg(o.dir);
    let deps: Map<string, string>;
    const notInstalled: string[] = [];
    try {
      deps = tree(o.dir);
    } catch {
      // layout-vue's deps live in the root install; resolve them from this spike's node_modules instead.
      // @vueuse/core is declared there but never imported, so the spike doesn't install it.
      deps = new Map();
      for (const dep of Object.keys(own.dependencies ?? {}).filter((d) => !d.startsWith('@chadash/') && d !== 'vue')) {
        let d: string;
        try {
          d = resolveDir(dep, ROOT);
        } catch {
          notInstalled.push(dep);
          continue;
        }
        const p = readPkg(d);
        deps.set(`${p.name}@${p.version}`, p.license ?? 'unknown');
        tree(d, deps);
      }
    }
    const licences = [...new Set(deps.values())].join(', ') || '—';
    const note = notInstalled.length ? ` (plus ${notInstalled.join(', ')}: declared, never imported)` : '';
    console.log(`| ${own.name}@${own.version} | ${own.license} | ${deps.size}${note} | ${licences} |`);
    if (process.env.VERBOSE) console.log(`|   | | | ${[...deps.keys()].join(', ')} |`);
  }
}
