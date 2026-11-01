// Shared helpers for the docs site's generators: they turn the repository's
// Markdown (READMEs, recipes, docs/) into VitePress pages, so the site moves
// with the code instead of keeping copies.
import { existsSync, statSync } from 'node:fs';
import { dirname, join, normalize, posix, resolve } from 'node:path';

/** The repository's root, two levels above this file. */
export const ROOT = resolve(import.meta.dir, '../..');
/** The docs site's own directory. */
export const SITE = resolve(import.meta.dir, '..');
/** Where a link to a file that isn't on the site goes. */
export const REPO_URL = 'https://github.com/ban-red/cha-dash';

/** A page to write: a path under docs-site/ and its Markdown. */
export interface Page {
  path: string;
  content: string;
}

/**
 * Routes maps a repository path (relative, with forward slashes) to the site
 * route that shows it. An anchor can move to another route: deploy/README.md
 * is split across two pages.
 */
export class Routes {
  private files = new Map<string, string>();
  private anchors = new Map<string, string>();

  add(repoPath: string, route: string): this {
    this.files.set(repoPath, route);
    return this;
  }

  /** Sends repoPath#anchor to route instead of the file's own route. */
  addAnchor(repoPath: string, anchor: string, route: string): this {
    this.anchors.set(`${repoPath}#${anchor}`, route);
    return this;
  }

  route(repoPath: string, anchor: string): string | undefined {
    if (anchor) {
      const moved = this.anchors.get(`${repoPath}#${anchor}`);
      if (moved) return moved;
    }
    return this.files.get(repoPath);
  }
}

/** Splits YAML front matter from a Markdown file. */
export function frontMatter(text: string): { data: Record<string, unknown>; body: string } {
  const m = /^---\r?\n([\s\S]*?)\r?\n---\r?\n?/.exec(text);
  if (!m) return { data: {}, body: text };
  const data = (Bun.YAML.parse(m[1]!) ?? {}) as Record<string, unknown>;
  return { data, body: text.slice(m[0].length) };
}

/**
 * Calls fn on each stretch of Markdown outside fenced code blocks, and
 * leaves the fences as they are.
 */
export function outsideFences(md: string, fn: (text: string) => string): string {
  const out: string[] = [];
  let buf: string[] = [];
  let fence: string | null = null;
  const flush = () => {
    if (buf.length) out.push(fn(buf.join('\n')));
    buf = [];
  };
  for (const line of md.split('\n')) {
    const f = /^\s*(`{3,}|~{3,})/.exec(line);
    if (fence === null && f) {
      flush();
      fence = f[1]!;
      out.push(line);
    } else if (fence !== null) {
      out.push(line);
      if (line.trim().startsWith(fence) && line.trim().replace(/[`~]/g, '') === '') fence = null;
    } else {
      buf.push(line);
    }
  }
  flush();
  return out.join('\n');
}

/** Calls fn on text outside inline code spans. */
function outsideCode(text: string, fn: (s: string) => string): string {
  return text
    .split(/(`+[^`]*?`+)/)
    .map((part, i) => (i % 2 === 1 ? part : fn(part)))
    .join('');
}

// HTML a source may use on purpose. Anything else that looks like a tag
// (<id>, <host>, <guest>) is text, and VitePress would read it as an
// unclosed element, so it's escaped.
const HTML_TAGS = new Set([
  'a', 'abbr', 'b', 'br', 'code', 'details', 'div', 'em', 'i', 'img', 'kbd', 'li', 'ol', 'p', 'picture',
  'pre', 'source', 'span', 'strong', 'sub', 'summary', 'sup', 'table', 'tbody', 'td', 'th', 'thead', 'tr', 'ul',
]);

const VOID_TAGS = new Set(['br', 'img', 'source']);

/** Escapes a `<` that would open an element VitePress doesn't expect. */
export function escapeTags(md: string): string {
  const keep = (name: string): boolean => {
    const n = name.toLowerCase();
    if (!HTML_TAGS.has(n)) return false;
    // An element kept as HTML must be closed somewhere, or Vue fails on it.
    return VOID_TAGS.has(n) || new RegExp(`</${n}\\s*>`, 'i').test(md);
  };
  return outsideFences(md, (text) =>
    outsideCode(text, (s) =>
      s.replace(/<(\/?)([A-Za-z][\w-]*)(?=([\s>/]|$))/g, (all, _slash: string, name: string) =>
        keep(name) ? all : `&lt;${all.slice(1)}`,
      ),
    ),
  );
}

/** A GitHub-style heading anchor, the kind the repository's links use. */
export function githubSlug(heading: string): string {
  return heading
    .trim()
    .toLowerCase()
    .replace(/[^\p{L}\p{N}\s_-]/gu, '')
    .replace(/\s/g, '-');
}

/** A link to a repository path on GitHub: a tree for a folder, a blob for a file. */
export function githubURL(repoPath: string, anchor = ''): string {
  const kind = existsSync(join(ROOT, repoPath)) && statSync(join(ROOT, repoPath)).isDirectory() ? 'tree' : 'blob';
  const clean = repoPath.replace(/\/$/, '');
  return `${REPO_URL}/${kind}/main/${clean}${anchor ? `#${anchor}` : ''}`;
}

/**
 * Rewrites relative links in Markdown from sourcePath (a repository path).
 * A link to a file the site shows goes to its route; any other relative link
 * goes to the file on GitHub. Absolute URLs and mailto: stay; a bare #anchor
 * stays unless selfRoute is given and the anchor lives on another route.
 */
export function rewriteLinks(md: string, sourcePath: string, routes: Routes, selfRoute?: string): string {
  const dir = posix.dirname(sourcePath);
  const fix = (target: string): string => {
    if (target.startsWith('#') && selfRoute) {
      // A file split across pages: the section may be on the other one.
      const route = routes.route(sourcePath, target.slice(1));
      return route && route !== selfRoute ? `${route}${target}` : target;
    }
    if (/^([a-z][a-z0-9+.-]*:|#|\/\/)/i.test(target)) return target;
    const [pathPart, anchor = ''] = target.split('#') as [string, string?];
    if (pathPart === '') return target;
    const repoPath = normalize(posix.join(dir, pathPart)).replace(/\\/g, '/');
    if (repoPath.startsWith('..')) return target;
    const route = routes.route(repoPath.replace(/\/$/, ''), anchor);
    if (route) return anchor ? `${route}#${anchor}` : route;
    return githubURL(repoPath, anchor);
  };
  return outsideFences(md, (text) =>
    outsideCode(text, (s) =>
      s
        // [text](target "title") and ![alt](target)
        .replace(/(\]\()(<[^>]+>|[^)\s]+)((?:\s+"[^"]*")?\))/g, (_all, open: string, target: string, close: string) => {
          const bare = target.startsWith('<') ? target.slice(1, -1) : target;
          return `${open}${fix(bare)}${close}`;
        })
        // [label]: target
        .replace(/^(\s*\[[^\]]+\]:\s+)(\S+)/gm, (_all, head: string, target: string) => `${head}${fix(target)}`),
    ),
  );
}

/** Moves every heading down by n levels, never past h6. */
export function demoteHeadings(md: string, n = 1): string {
  return outsideFences(md, (text) =>
    text.replace(/^(#{1,6})(?=\s)/gm, (h) => '#'.repeat(Math.min(6, h.length + n))),
  );
}

/** Drops the first h1 and returns its text. */
export function takeTitle(md: string): { title: string; body: string } {
  const m = /^#\s+(.+)\n?/m.exec(md);
  if (!m) return { title: '', body: md };
  return { title: m[1]!.trim(), body: md.slice(0, m.index) + md.slice(m.index + m[0].length) };
}

/** Splits Markdown at its h2 headings. The part before the first is under ''. */
export function sections(md: string): { heading: string; text: string }[] {
  const out: { heading: string; text: string }[] = [{ heading: '', text: '' }];
  const lines = md.split('\n');
  let fence: string | null = null;
  for (const line of lines) {
    const f = /^\s*(`{3,}|~{3,})/.exec(line);
    if (fence === null && f) fence = f[1]!;
    else if (fence !== null && line.trim().startsWith(fence)) fence = null;
    else if (fence === null && /^##\s/.test(line)) {
      out.push({ heading: line.replace(/^##\s+/, '').trim(), text: '' });
    }
    const last = out[out.length - 1]!;
    last.text += `${line}\n`;
  }
  return out;
}

/**
 * Wraps a generated page so VitePress shows it as written: `{{ }}` in a
 * recipe or a spec is text, never a Vue expression. The note at the end
 * names the file it came from.
 */
export function page(opts: { title: string; body: string; sources: string[]; description?: string }): string {
  const fm = opts.description ? `---\ndescription: ${JSON.stringify(opts.description)}\n---\n\n` : '';
  const from = opts.sources.map((s) => `[\`${s}\`](${githubURL(s)})`).join(', ');
  return [
    `${fm}::: v-pre`,
    '',
    `# ${opts.title}`,
    '',
    opts.body.trim(),
    '',
    ':::',
    '',
    `*Generated from ${from}. Edit it there.*`,
    '',
  ].join('\n');
}

/** Reads a repository file as text. */
export async function read(repoPath: string): Promise<string> {
  return Bun.file(join(ROOT, repoPath)).text();
}

/** Writes pages under docs-site/, making folders as needed. */
export async function writePages(pages: Page[]): Promise<void> {
  const { mkdir } = await import('node:fs/promises');
  for (const p of pages) {
    const file = join(SITE, p.path);
    await mkdir(dirname(file), { recursive: true });
    await Bun.write(file, p.content);
  }
}

/** Escapes a value for a Markdown table cell. */
export function cell(s: unknown): string {
  return String(s ?? '')
    .replace(/\|/g, '\\|')
    .replace(/\n+/g, ' ');
}
