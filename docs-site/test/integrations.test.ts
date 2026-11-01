import { describe, expect, test } from 'bun:test';
import { existsSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import {
  addIntegrationRoutes,
  integrationIndex,
  integrationPage,
  listIntegrations,
  lowerFirst,
  recipeSection,
  specNotes,
} from '../scripts/integrations.ts';
import { ROOT, Routes, read, sections } from '../scripts/lib.ts';
import { PROSE, UPGRADE_SECTIONS, addProseRoutes } from '../scripts/pages.ts';

describe('every integration gets a page', () => {
  const list = listIntegrations();

  test('one per integration folder and per bundled spec', () => {
    const goIDs = readdirSync(join(ROOT, 'integrations')).filter(
      (d) => d !== 'internal' && existsSync(join(ROOT, 'integrations', d, 'README.md')),
    );
    const specIDs = readdirSync(join(ROOT, 'specs'))
      .filter((f) => f.endsWith('.yaml'))
      .map((f) => f.replace(/\.yaml$/, ''));
    expect(list.filter((i) => i.kind === 'go').map((i) => i.id)).toEqual(goIDs.sort());
    expect(list.filter((i) => i.kind === 'spec').map((i) => i.id)).toEqual(specIDs.sort());
    expect(new Set(list.map((i) => i.id)).size).toBe(list.length);
  });

  test('each page builds, with a title, a credential section and its sources', async () => {
    const routes = new Routes();
    addIntegrationRoutes(routes, list);
    await addProseRoutes(routes);
    for (const i of list) {
      const p = await integrationPage(i, routes);
      expect(p.path).toBe(`integrations/${i.id}.md`);
      expect(p.content).toContain(`# ${i.name}\n`);
      expect(p.content).toContain('## Credentials');
      expect(i.summary).not.toBe('');
      expect(p.content).toContain('::: v-pre');
    }
    const index = integrationIndex(list);
    for (const i of list) expect(index.content).toContain(`(/integrations/${i.id})`);
  });
});

describe('recipes', () => {
  test('parameters are filled with their defaults; other templates stay', () => {
    const text = `---
title: A token
summary: Reads only.
access: read-only
params:
  - name: user
    label: User
    default: chadash@pve
  - name: privs
    label: Version
    options:
      - {label: '9.x', value: 'A B'}
---

## Step

\`\`\`sh
pveum user add {{user}} --privs "{{ privs }}"
docker network inspect x --format '{{.Subnet}}'
\`\`\`
`;
    const r = recipeSection(text, 'Credentials', 'integrations/x/recipe.md', new Routes());
    expect(r.summary).toBe('Reads only.');
    expect(r.md).toContain('## Credentials: a token');
    expect(r.md).toContain('### Step');
    expect(r.md).toContain('pveum user add chadash@pve --privs "A B"');
    expect(r.md).toContain("'{{.Subnet}}'");
    expect(r.md).toContain('| Access | `read-only` |');
  });

  test('lowerFirst keeps acronyms', () => {
    expect(lowerFirst('The API key')).toBe('the API key');
    expect(lowerFirst('A control token')).toBe('a control token');
    expect(lowerFirst('API key')).toBe('API key');
  });

  test('specNotes takes the bold-named paragraph and its list', () => {
    const md = 'intro\n\n**Alpha.** One.\n- detail\n\n**Beta.** Two.\n';
    expect(specNotes(md, 'Alpha')).toBe('**Alpha.** One.\n- detail');
    expect(specNotes(md, 'Gamma')).toBe('');
  });
});

describe('prose pages', () => {
  test('deploy/README.md still has the sections the Upgrade page takes', async () => {
    const headings = sections(await read('deploy/README.md')).map((s) => s.heading);
    for (const h of UPGRADE_SECTIONS) expect(headings).toContain(h);
  });

  test('every required source exists', () => {
    for (const p of PROSE) if (!p.optional) expect(existsSync(join(ROOT, p.source))).toBe(true);
  });
});
