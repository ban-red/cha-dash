// The docs site's configuration. Most pages are generated from the
// repository by scripts/gen.ts, which also writes generated.json: the
// integration and API entries for the sidebar.
import { existsSync, readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vitepress';

interface Entry {
  text: string;
  link: string;
}

const generatedFile = fileURLToPath(new URL('./generated.json', import.meta.url));
if (!existsSync(generatedFile)) {
  throw new Error('docs-site/.vitepress/generated.json is missing: run `bun run gen` in docs-site first');
}
const generated = JSON.parse(readFileSync(generatedFile, 'utf8')) as { integrations: Entry[]; api: Entry[] };

const guide = [
  {
    text: 'Get started',
    items: [
      { text: 'Install', link: '/install' },
      { text: 'Upgrade and back up', link: '/upgrade' },
      { text: 'Unraid', link: '/unraid' },
      { text: 'User guide', link: '/guide' },
    ],
  },
  {
    text: 'Extend',
    items: [
      { text: 'Integrations', link: '/integrations/' },
      { text: 'Writing specs', link: '/specs' },
      { text: 'Agents', link: '/agents' },
      { text: 'API', link: '/api/' },
    ],
  },
  {
    text: 'Security',
    items: [
      { text: 'Security model', link: '/security' },
      { text: 'Threat model', link: '/threat-model' },
    ],
  },
];

export default defineConfig({
  title: 'Cha Dash',
  description: 'A calm, live page for your homelab that can also act on it.',
  lang: 'en',
  // GitHub Pages serves the project at /cha-dash/. DOCS_BASE overrides it,
  // for a custom domain ("/") or a preview.
  base: process.env.DOCS_BASE ?? '/cha-dash/',
  cleanUrls: true,
  // Links between pages are checked at build time; a dead one fails it.
  ignoreDeadLinks: false,
  themeConfig: {
    nav: [
      { text: 'Install', link: '/install' },
      { text: 'Integrations', link: '/integrations/' },
      { text: 'API', link: '/api/' },
      { text: 'Security', link: '/security' },
    ],
    sidebar: {
      '/integrations/': [
        { text: 'Integrations', link: '/integrations/', items: generated.integrations },
        { text: 'Writing specs', link: '/specs' },
      ],
      '/api/': [{ text: 'API', link: '/api/', items: generated.api }],
      '/': guide,
    },
    outline: [2, 3],
    search: { provider: 'local' },
    socialLinks: [{ icon: 'github', link: 'https://github.com/ban-red/cha-dash' }],
    footer: {
      message: 'AGPL-3.0-or-later. Generated from the repository on every push to main.',
    },
  },
});
