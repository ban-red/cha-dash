import { describe, expect, test } from 'bun:test';
import {
  Routes,
  demoteHeadings,
  escapeTags,
  frontMatter,
  githubSlug,
  page,
  rewriteLinks,
  sections,
  takeTitle,
} from '../scripts/lib.ts';

const routes = () =>
  new Routes()
    .add('deploy/README.md', '/install')
    .add('SECURITY.md', '/security')
    .addAnchor('deploy/README.md', 'upgrading', '/upgrade');

describe('rewriteLinks', () => {
  test('a file the site shows goes to its route', () => {
    expect(rewriteLinks('[s](../SECURITY.md)', 'docs/beta.md', routes())).toBe('[s](/security)');
    expect(rewriteLinks('[d](../deploy/README.md#the-node)', 'docs/beta.md', routes())).toBe('[d](/install#the-node)');
  });

  test('an anchor that moved to another page follows it', () => {
    expect(rewriteLinks('[u](../deploy/README.md#upgrading)', 'docs/beta.md', routes())).toBe('[u](/upgrade#upgrading)');
    expect(rewriteLinks('[u](#upgrading)', 'deploy/README.md', routes(), '/install')).toBe('[u](/upgrade#upgrading)');
    expect(rewriteLinks('[n](#the-node)', 'deploy/README.md', routes(), '/upgrade')).toBe('[n](/install#the-node)');
    expect(rewriteLinks('[n](#the-node)', 'deploy/README.md', routes(), '/install')).toBe('[n](#the-node)');
  });

  test('any other file goes to GitHub, folders as trees', () => {
    expect(rewriteLinks('[a](adr/0017-ai-and-mcp.md)', 'docs/x.md', routes())).toBe(
      '[a](https://github.com/ban-red/cha-dash/blob/main/docs/adr/0017-ai-and-mcp.md)',
    );
    expect(rewriteLinks('[v](../internal/vault)', 'docs/x.md', routes())).toBe(
      '[v](https://github.com/ban-red/cha-dash/tree/main/internal/vault)',
    );
  });

  test('absolute links, bare anchors and code stay', () => {
    const md = '[g](https://example.com/a) [h](#here) `[c](x.md)`\n\n```md\n[f](y.md)\n```';
    expect(rewriteLinks(md, 'docs/x.md', routes())).toBe(md);
  });

  test('reference-style links are rewritten too', () => {
    expect(rewriteLinks('[s]: ../SECURITY.md', 'docs/x.md', routes())).toBe('[s]: /security');
  });
});

describe('escapeTags', () => {
  test('placeholders that look like tags become text', () => {
    expect(escapeTags('through <guest>\'s backup')).toBe('through &lt;guest>\'s backup');
    expect(escapeTags('<a long password>')).toBe('&lt;a long password>');
  });

  test('real HTML, code and autolinks stay', () => {
    const md = '<details><summary>x</summary>y</details> `<id>` <https://example.com> <br>\n```\n<id>\n```';
    expect(escapeTags(md)).toBe(md);
  });
});

describe('headings and sections', () => {
  test('demoteHeadings skips code', () => {
    expect(demoteHeadings('# A\n```sh\n# comment\n```\n## B')).toBe('## A\n```sh\n# comment\n```\n### B');
  });

  test('takeTitle drops the first h1', () => {
    expect(takeTitle('# Title (`x`)\n\nBody')).toEqual({ title: 'Title (`x`)', body: '\nBody' });
  });

  test('sections splits at h2 outside code', () => {
    const s = sections('intro\n## One\n```\n## not a heading\n```\n## Two\nend\n');
    expect(s.map((x) => x.heading)).toEqual(['', 'One', 'Two']);
    expect(s[1]!.text).toContain('## not a heading');
  });

  test('githubSlug matches GitHub', () => {
    expect(githubSlug('Keeping the key outside `/data`')).toBe('keeping-the-key-outside-data');
    expect(githubSlug('Behind a reverse proxy')).toBe('behind-a-reverse-proxy');
  });
});

test('frontMatter parses YAML and returns the body', () => {
  const { data, body } = frontMatter('---\ntitle: T\ngrants: [a, b]\n---\n\n## Step\n');
  expect(data).toEqual({ title: 'T', grants: ['a', 'b'] });
  expect(body).toBe('\n## Step\n');
});

test('page wraps the body so {{ }} is text, and names its source', () => {
  const p = page({ title: 'X', body: 'run {{ secret.key }}', sources: ['specs/x.yaml'] });
  expect(p).toContain('::: v-pre\n\n# X\n\nrun {{ secret.key }}\n\n:::');
  expect(p).toContain('https://github.com/ban-red/cha-dash/blob/main/specs/x.yaml');
});
