// Generates the docs site's pages from the repository, before every build:
//
//   bun run gen      (in docs-site/; docs:dev and docs:build run it first)
//
// Everything it writes is git-ignored (docs-site/.gitignore). Only index.md
// and .vitepress/config.ts are written by hand.
import { existsSync } from 'node:fs';
import { rm } from 'node:fs/promises';
import { join } from 'node:path';
import { type OpenAPI, apiPages } from './api.ts';
import { addIntegrationRoutes, integrationPages, listIntegrations } from './integrations.ts';
import { Routes, SITE, writePages } from './lib.ts';
import { addProseRoutes, prosePages } from './pages.ts';

const OPENAPI = join(SITE, 'public/openapi.json');

if (!existsSync(OPENAPI)) {
  console.error(`docs-site/public/openapi.json is missing. Write it from the Go code first:

  CHADASH_WRITE_OPENAPI=$PWD/docs-site/public/openapi.json go test ./internal/apidoc/ -run TestWriteOpenAPI
`);
  process.exit(1);
}

const routes = new Routes();
const list = listIntegrations();
addIntegrationRoutes(routes, list);
await addProseRoutes(routes);

// Generated folders are rebuilt from scratch, so a removed integration or
// API area doesn't leave a page behind.
await rm(join(SITE, 'integrations'), { recursive: true, force: true });
await rm(join(SITE, 'api'), { recursive: true, force: true });

const prose = await prosePages(routes);
const integrations = await integrationPages(routes, list);
const api = apiPages((await Bun.file(OPENAPI).json()) as OpenAPI);

await writePages([...prose, ...integrations.pages, ...api.pages]);
await Bun.write(
  join(SITE, '.vitepress/generated.json'),
  `${JSON.stringify({ integrations: integrations.sidebar, api: api.sidebar }, null, 2)}\n`,
);

console.log(
  `docs-site: ${prose.length} prose pages, ${integrations.pages.length} integration pages, ${api.pages.length} API pages`,
);
