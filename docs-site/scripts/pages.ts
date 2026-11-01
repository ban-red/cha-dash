// The docs site's prose pages, generated from the repository's own Markdown:
// deploy/README.md (split into Install and Upgrade), the Unraid README, the
// user guide (docs/beta.md), docs/specs.md, SECURITY.md, the threat model and,
// once it exists, docs/agents.md.
import { existsSync } from 'node:fs';
import { join } from 'node:path';
import {
  type Page,
  ROOT,
  Routes,
  escapeTags,
  githubSlug,
  page,
  read,
  rewriteLinks,
  sections,
  takeTitle,
} from './lib.ts';

/** The deploy/README.md sections that go on the Upgrade page; the rest are Install. */
export const UPGRADE_SECTIONS = ['Upgrading', 'Backing up', 'Uninstalling'];

/** One-to-one pages: a repository file, its route and the page's title. */
export const PROSE: { source: string; route: string; title: string; optional?: boolean }[] = [
  { source: 'deploy/unraid/README.md', route: '/unraid', title: 'Unraid' },
  { source: 'docs/beta.md', route: '/guide', title: 'User guide' },
  { source: 'docs/specs.md', route: '/specs', title: 'Writing specs' },
  { source: 'SECURITY.md', route: '/security', title: 'Security model' },
  { source: 'docs/threat-model.md', route: '/threat-model', title: 'Threat model' },
  { source: 'docs/agents.md', route: '/agents', title: 'Agents', optional: true },
];

/** Adds the prose pages' routes, so links between them stay on the site. */
export async function addProseRoutes(routes: Routes): Promise<void> {
  routes.add('deploy/README.md', '/install').add('deploy', '/install');
  const deploy = await read('deploy/README.md');
  for (const s of sections(deploy)) {
    if (UPGRADE_SECTIONS.includes(s.heading)) {
      routes.addAnchor('deploy/README.md', githubSlug(s.heading), '/upgrade');
      for (const sub of s.text.matchAll(/^###\s+(.+)$/gm)) {
        routes.addAnchor('deploy/README.md', githubSlug(sub[1]!), '/upgrade');
      }
    }
  }
  for (const p of PROSE) {
    if (!p.optional || existsSync(join(ROOT, p.source))) routes.add(p.source, p.route);
  }
}

/** Turns a repository Markdown file's body into page Markdown. */
function prose(md: string, source: string, routes: Routes, self?: string): string {
  return escapeTags(rewriteLinks(md, source, routes, self));
}

/** The prose pages. Install and Upgrade split deploy/README.md at its h2s. */
export async function prosePages(routes: Routes): Promise<Page[]> {
  const pages: Page[] = [];

  const deploy = takeTitle(await read('deploy/README.md')).body;
  const parts = sections(deploy);
  const missing = UPGRADE_SECTIONS.filter((h) => !parts.some((s) => s.heading === h));
  if (missing.length) {
    throw new Error(`deploy/README.md has no section ${missing.map((m) => `"## ${m}"`).join(', ')}: update UPGRADE_SECTIONS in docs-site/scripts/pages.ts`);
  }
  const install = parts.filter((s) => !UPGRADE_SECTIONS.includes(s.heading)).map((s) => s.text).join('');
  const upgrade = parts.filter((s) => UPGRADE_SECTIONS.includes(s.heading)).map((s) => s.text).join('');
  pages.push(
    { path: 'install.md', content: page({ title: 'Install', body: prose(install, 'deploy/README.md', routes, '/install'), sources: ['deploy/README.md'] }) },
    {
      path: 'upgrade.md',
      content: page({
        title: 'Upgrade and back up',
        body: prose(upgrade, 'deploy/README.md', routes, '/upgrade'),
        sources: ['deploy/README.md'],
      }),
    },
  );

  for (const p of PROSE) {
    const file = join(ROOT, p.source);
    if (!existsSync(file)) {
      if (!p.optional) throw new Error(`${p.source} is missing`);
      pages.push({ path: `${p.route.slice(1)}.md`, content: agentsPlaceholder() });
      continue;
    }
    const { body } = takeTitle(await read(p.source));
    pages.push({
      path: `${p.route.slice(1)}.md`,
      content: page({ title: p.title, body: prose(body, p.source, routes), sources: [p.source] }),
    });
  }
  return pages;
}

/** Shown until docs/agents.md exists. */
function agentsPlaceholder(): string {
  return `# Agents

Agents (MCP) arrive in 0.4. An agent reads and proposes; a person approves every write. The rules are in [ADR-0017](https://github.com/ban-red/cha-dash/blob/main/docs/adr/0017-ai-and-mcp.md), and the [threat model](/threat-model) covers them.

This page is generated from \`docs/agents.md\` once it exists.
`;
}
