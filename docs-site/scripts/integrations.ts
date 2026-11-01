// One docs page per integration, generated so a new integration has a page
// without anyone writing one:
//   - built in (Go): integrations/<id>/README.md, recipe.md and, when there
//     is one, recipe.control.md;
//   - specs (YAML): specs/<id>.yaml, specs/recipes/<id>.md and the spec's
//     notes in specs/README.md.
import { existsSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import {
  type Page,
  ROOT,
  Routes,
  cell,
  demoteHeadings,
  escapeTags,
  frontMatter,
  githubURL,
  page,
  read,
  rewriteLinks,
  takeTitle,
} from './lib.ts';

export interface Integration {
  id: string;
  name: string;
  kind: 'go' | 'spec';
  /** The recipe's summary: what the credential can do. */
  summary: string;
  /** Repository paths the page is built from. */
  sources: string[];
}

/** The integrations in the repository, sorted by name. */
export function listIntegrations(): Integration[] {
  const out: Integration[] = [];
  for (const id of readdirSync(join(ROOT, 'integrations')).sort()) {
    const readme = `integrations/${id}/README.md`;
    if (id === 'internal' || !existsSync(join(ROOT, readme))) continue;
    const sources = [readme, `integrations/${id}/recipe.md`, `integrations/${id}/recipe.control.md`].filter((s) =>
      existsSync(join(ROOT, s)),
    );
    out.push({ id, name: id, kind: 'go', summary: '', sources });
  }
  for (const file of readdirSync(join(ROOT, 'specs')).sort()) {
    if (!file.endsWith('.yaml')) continue;
    const id = file.slice(0, -'.yaml'.length);
    const sources = [`specs/${file}`, `specs/recipes/${id}.md`].filter((s) => existsSync(join(ROOT, s)));
    out.push({ id, name: id, kind: 'spec', summary: '', sources });
  }
  return out;
}

/** Sends every file of an integration to its page. */
export function addIntegrationRoutes(routes: Routes, list: Integration[]): void {
  for (const i of list) {
    const route = `/integrations/${i.id}`;
    if (i.kind === 'go') routes.add(`integrations/${i.id}`, route);
    for (const s of i.sources) routes.add(s, route);
  }
}

interface RecipeParam {
  name: string;
  label?: string;
  default?: string;
  help?: string;
  options?: { label: string; value: string }[];
}

/**
 * A recipe as a section: its title, what the credential can do, the
 * parameters it takes, and its steps with each {{param}} filled with its
 * default, the way the hub's form starts.
 */
export function recipeSection(text: string, heading: string, source: string, routes: Routes): { md: string; summary: string } {
  const { data, body } = frontMatter(text);
  const params = (data.params as RecipeParam[] | undefined) ?? [];
  const values = new Map<string, string>();
  for (const p of params) {
    const v = p.default ?? p.options?.[0]?.value;
    if (v !== undefined) values.set(p.name, v);
  }
  const filled = body.replace(/\{\{\s*([A-Za-z_]\w*)\s*\}\}/g, (all, name: string) => values.get(name) ?? all);
  const lines: string[] = [`## ${heading}${data.title ? `: ${lowerFirst(String(data.title))}` : ''}`, ''];
  if (data.summary) lines.push(String(data.summary), '');
  const facts: [string, string][] = [];
  if (data.access) facts.push(['Access', `\`${String(data.access)}\``]);
  if (Array.isArray(data.grants) && data.grants.length) facts.push(['Grants', data.grants.map((g) => `\`${String(g)}\``).join(', ')]);
  if (facts.length) {
    lines.push('| | |', '|---|---|', ...facts.map(([k, v]) => `| ${k} | ${cell(v)} |`), '');
  }
  if (params.length) {
    lines.push(
      'The steps below use these defaults; the hub\'s form lets you change them.',
      '',
      '| Parameter | Default | Notes |',
      '|---|---|---|',
      ...params.map((p) => {
        const d = p.default ?? (p.options ? p.options.map((o) => `${o.label}: \`${o.value}\``).join('; ') : '');
        const def = p.default !== undefined ? `\`${p.default}\`` : d;
        return `| ${cell(p.label ?? p.name)} | ${cell(def)} | ${cell(p.help ?? '')} |`;
      }),
      '',
    );
  }
  lines.push(demoteHeadings(rewriteLinks(filled, source, routes), 1).trim(), '');
  return { md: lines.join('\n'), summary: String(data.summary ?? '') };
}

/** "The API key" → "the API key"; "API key" stays. */
export function lowerFirst(s: string): string {
  return /^[A-Z](?![A-Z])/.test(s) ? s[0]!.toLowerCase() + s.slice(1) : s;
}

interface SpecConfigField {
  type?: string;
  title?: string;
  required?: boolean;
  description?: string;
  default?: unknown;
}

/** The notes for one spec in specs/README.md: its bold-named paragraph and any list under it. */
export function specNotes(readme: string, name: string): string {
  const lines = readme.split('\n');
  const start = lines.findIndex((l) => l.startsWith(`**${name}.**`));
  if (start < 0) return '';
  const out = [lines[start]!];
  for (const l of lines.slice(start + 1)) {
    if (/^\*\*[^*]+\.\*\*/.test(l) || /^#{1,6}\s/.test(l)) break;
    out.push(l);
  }
  return out.join('\n').trim();
}

/** A spec's facts from its YAML: what it reads and how it's set up. */
function specSection(spec: Record<string, unknown>, id: string): { name: string; md: string } {
  const meta = (spec.meta ?? {}) as Record<string, unknown>;
  const name = String(meta.name ?? id);
  const caps = (meta.capabilities as string[] | undefined) ?? [];
  const rows: [string, string][] = [
    ['ID', `\`${id}\``],
    ['Kind', 'Spec (YAML), bundled with the hub'],
    ['Version', `\`${String(meta.version ?? '')}\``],
    ['Tier', String(meta.tier ?? '')],
    ['Category', String(meta.category ?? '')],
    ['Capabilities', caps.map((c) => `\`${c}\``).join(', ')],
    ['Polls every', spec.every ? `\`${String(spec.every)}\`` : ''],
  ];
  const lines = ['| | |', '|---|---|', ...rows.filter(([, v]) => v).map(([k, v]) => `| ${k} | ${cell(v)} |`), ''];
  const config = (spec.config ?? {}) as Record<string, SpecConfigField>;
  lines.push('## Config', '', '| Field | Type | Required | Notes |', '|---|---|---|---|');
  lines.push('| `url` | url | yes | Where it is, with its URL base if it has one |');
  for (const [field, f] of Object.entries(config)) {
    const notes = [f.title, f.description, f.default !== undefined ? `Default \`${String(f.default)}\`` : '']
      .filter(Boolean)
      .join('. ');
    lines.push(`| \`${field}\` | ${cell(f.type ?? '')} | ${f.required ? 'yes' : 'no'} | ${cell(notes)} |`);
  }
  lines.push('');
  const requests = (spec.requests ?? {}) as Record<string, { path?: string; method?: string; optional?: boolean }>;
  if (Object.keys(requests).length) {
    lines.push('## What it reads', '', '| Request | Path | |', '|---|---|---|');
    for (const [rid, r] of Object.entries(requests)) {
      lines.push(`| \`${rid}\` | \`${(r.method ?? 'GET').toUpperCase()} ${cell(r.path ?? '')}\` | ${r.optional ? 'optional' : ''} |`);
    }
    lines.push('');
  }
  // actions is a list of {id, title, tier}; a map keyed by ID reads the same.
  const rawActions = spec.actions ?? [];
  const actions: Record<string, { title?: string; tier?: string }> = Array.isArray(rawActions)
    ? Object.fromEntries((rawActions as { id?: string; title?: string; tier?: string }[]).map((a, n) => [a.id ?? String(n), a]))
    : (rawActions as Record<string, { title?: string; tier?: string }>);
  if (Object.keys(actions).length) {
    lines.push(
      '## Actions',
      '',
      'Off until the owner turns them on for the installed spec.',
      '',
      '| Action | Tier | |',
      '|---|---|---|',
    );
    for (const [aid, a] of Object.entries(actions)) {
      lines.push(`| \`${aid}\` | ${cell(a.tier ?? '')} | ${cell(a.title ?? '')} |`);
    }
    lines.push('');
  }
  return { name, md: lines.join('\n') };
}

/** Builds one integration's page and fills in its name and summary. */
export async function integrationPage(i: Integration, routes: Routes): Promise<Page> {
  const parts: string[] = [];
  if (i.kind === 'go') {
    const readmePath = `integrations/${i.id}/README.md`;
    const { title, body } = takeTitle(await read(readmePath));
    i.name = title.replace(/\s*\(`[^`]+`\)\s*$/, '').trim() || i.id;
    parts.push(
      '| | |',
      '|---|---|',
      `| ID | \`${i.id}\` |`,
      '| Kind | Built in (Go) |',
      `| Source | [\`integrations/${i.id}/\`](${githubURL(`integrations/${i.id}`)}) |`,
      '',
    );
    const recipe = `integrations/${i.id}/recipe.md`;
    if (existsSync(join(ROOT, recipe))) {
      const r = recipeSection(await read(recipe), 'Credentials', recipe, routes);
      i.summary = r.summary;
      parts.push(r.md);
    }
    const control = `integrations/${i.id}/recipe.control.md`;
    if (existsSync(join(ROOT, control))) {
      parts.push(recipeSection(await read(control), 'Control credentials', control, routes).md);
    }
    parts.push('## Reference', '', demoteHeadings(rewriteLinks(body, readmePath, routes), 1).trim(), '');
  } else {
    const specPath = `specs/${i.id}.yaml`;
    const spec = (Bun.YAML.parse(await read(specPath)) ?? {}) as Record<string, unknown>;
    const s = specSection(spec, i.id);
    i.name = s.name;
    parts.push(s.md);
    const recipe = `specs/recipes/${i.id}.md`;
    if (existsSync(join(ROOT, recipe))) {
      const r = recipeSection(await read(recipe), 'Credentials', recipe, routes);
      i.summary = r.summary;
      parts.push(r.md);
    }
    const notes = specNotes(await read('specs/README.md'), s.name);
    if (notes) {
      parts.push('## Notes', '', rewriteLinks(notes, 'specs/README.md', routes), '');
      if (!i.sources.includes('specs/README.md')) i.sources.push('specs/README.md');
    }
  }
  return {
    path: `integrations/${i.id}.md`,
    content: page({ title: i.name, body: escapeTags(parts.join('\n')), sources: i.sources, description: i.summary || undefined }),
  };
}

/** The index: every integration, built in and specs, with what its credential can do. */
export function integrationIndex(list: Integration[]): Page {
  const byName = [...list].sort((a, b) => a.name.localeCompare(b.name));
  const rows = byName.map(
    (i) => `| [${cell(i.name)}](/integrations/${i.id}) | ${i.kind === 'go' ? 'Built in' : 'Spec'} | ${cell(i.summary)} |`,
  );
  const body = [
    `Cha Dash reads ${list.length} services out of the box: ${list.filter((i) => i.kind === 'go').length} built into the hub in Go, and ${list.filter((i) => i.kind === 'spec').length} as bundled specs (YAML). Each page says which credential to make, what it can do, and what Cha Dash reads with it.`,
    '',
    'A service that isn\'t here can be added as a spec: see [Writing specs](/specs).',
    '',
    '| Integration | Kind | The credential |',
    '|---|---|---|',
    ...rows,
    '',
  ].join('\n');
  return {
    path: 'integrations/index.md',
    content: page({ title: 'Integrations', body: escapeTags(body), sources: ['integrations', 'specs'] }),
  };
}

/** Every integration page, the index, and the sidebar's entries. */
export async function integrationPages(routes: Routes, list: Integration[]): Promise<{ pages: Page[]; sidebar: { text: string; link: string }[] }> {
  const pages: Page[] = [];
  for (const i of list) pages.push(await integrationPage(i, routes));
  pages.push(integrationIndex(list));
  const sidebar = [...list]
    .sort((a, b) => a.name.localeCompare(b.name))
    .map((i) => ({ text: i.name, link: `/integrations/${i.id}` }));
  return { pages, sidebar };
}
