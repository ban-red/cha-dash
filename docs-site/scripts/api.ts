// The API reference, rendered from the hub's OpenAPI document into plain
// Markdown: a page per tag and one for the schemas. No API-renderer
// JavaScript; the site stays static.
//
// The document comes from internal/apidoc, written by a Go test:
//   CHADASH_WRITE_OPENAPI=$PWD/docs-site/public/openapi.json go test ./internal/apidoc/ -run TestWriteOpenAPI
import { type Page, cell, escapeTags, page } from './lib.ts';

type Schema = Record<string, unknown>;

export interface OpenAPI {
  info: { title: string; version: string; description?: string };
  paths: Record<string, Record<string, Operation>>;
  components: { schemas: Record<string, Schema> };
}

interface Operation {
  operationId: string;
  summary?: string;
  tags?: string[];
  parameters?: { name: string; in: string; description?: string; required?: boolean; schema?: Schema }[];
  requestBody?: { content: Record<string, { schema?: Schema }> };
  responses: Record<string, { $ref?: string; description?: string; content?: Record<string, { schema?: Schema }> }>;
  'x-chadash-access'?: string;
  'x-chadash-scope'?: string;
  'x-chadash-errors'?: string[];
  'x-chadash-any-method'?: boolean;
}

const METHODS = ['get', 'post', 'put', 'patch', 'delete'];

/** The anchor of a schema on the schemas page. */
export function schemaAnchor(name: string): string {
  return `schema-${name.toLowerCase().replace(/[^a-z0-9]+/g, '-')}`;
}

/** A schema's type in one line: a link for a $ref, T[] for arrays, a | b for unions. */
export function typeOf(s: Schema | undefined): string {
  if (!s || Object.keys(s).length === 0) return 'any';
  if (typeof s.$ref === 'string') {
    const name = s.$ref.replace('#/components/schemas/', '');
    return `[${name}](/api/schemas#${schemaAnchor(name)})`;
  }
  for (const key of ['anyOf', 'oneOf'] as const) {
    if (Array.isArray(s[key])) return (s[key] as Schema[]).map(typeOf).join(' \\| ');
  }
  const t = s.type;
  const one = (name: string): string => {
    if (name === 'array') return `${typeOf(s.items as Schema | undefined)}[]`;
    if (name === 'object') {
      if (s.properties) return 'object';
      if (s.additionalProperties && typeof s.additionalProperties === 'object') {
        return `map of ${typeOf(s.additionalProperties as Schema)}`;
      }
      return 'object';
    }
    return s.format && name !== 'null' ? `${name} (${String(s.format)})` : name;
  };
  if (Array.isArray(t)) return t.map((x) => one(String(x))).join(' \\| ');
  if (typeof t === 'string') return one(t);
  return 'any';
}

/**
 * A schema's fields as table rows. Inline objects are listed with dotted
 * names (error.code), so a request body reads in one table.
 */
export function fieldRows(s: Schema, prefix = '', depth = 0): string[] {
  const props = (s.properties ?? {}) as Record<string, Schema>;
  const required = new Set((s.required as string[] | undefined) ?? []);
  const rows: string[] = [];
  for (const [name, p] of Object.entries(props)) {
    const full = `${prefix}${name}`;
    const desc = typeof p.description === 'string' ? p.description : '';
    rows.push(`| \`${full}\` | ${typeOf(p)} | ${required.has(name) ? 'yes' : ''} | ${cell(desc)} |`);
    if (depth < 3) {
      if (p.properties) rows.push(...fieldRows(p, `${full}.`, depth + 1));
      const items = p.items as Schema | undefined;
      if (p.type === 'array' && items?.properties) rows.push(...fieldRows(items, `${full}[].`, depth + 1));
    }
  }
  return rows;
}

/** A body's schema as Markdown: a field table for an inline object, else its type. */
function body(s: Schema | undefined): string {
  if (s && s.properties && Object.keys(s.properties as object).length) {
    return ['| Field | Type | Required | |', '|---|---|---|---|', ...fieldRows(s)].join('\n');
  }
  return typeOf(s);
}

function operationMd(method: string, path: string, op: Operation): string {
  const verb = op['x-chadash-any-method'] ? 'ANY' : method.toUpperCase();
  const lines = [`### \`${verb} ${path}\` {#${op.operationId}}`, ''];
  if (op.summary) lines.push(op.summary, '');
  const access = op['x-chadash-access'] ?? '';
  const scope = op['x-chadash-scope'];
  const who = [`**Access:** \`${access}\``];
  if (scope !== undefined) who.push(scope ? `**API token scope:** \`${scope}\`` : '**Session only**');
  lines.push(who.join(' · '), '');

  const params = op.parameters ?? [];
  if (params.length) {
    lines.push('| Parameter | In | Type | Required | |', '|---|---|---|---|---|');
    for (const p of params) {
      lines.push(`| \`${p.name}\` | ${p.in} | ${typeOf(p.schema)} | ${p.required ? 'yes' : ''} | ${cell(p.description ?? '')} |`);
    }
    lines.push('');
  }
  if (op.requestBody) {
    for (const [media, c] of Object.entries(op.requestBody.content)) {
      lines.push(`**Request** (\`${media}\`)`, '');
      if (media === 'application/json') lines.push(body(c.schema), '');
    }
  }
  const statuses = Object.keys(op.responses).filter((s) => s !== 'default').sort();
  for (const status of statuses) {
    const r = op.responses[status]!;
    const content = Object.entries(r.content ?? {});
    if (!content.length) {
      lines.push(`**${status}** ${r.description ?? ''}`.trim(), '');
      continue;
    }
    for (const [media, c] of content) {
      lines.push(`**${status}** (\`${media}\`)`, '');
      if (media === 'application/json') lines.push(body(c.schema), '');
    }
  }
  const errors = op['x-chadash-errors'];
  if (errors?.length) lines.push(`**Error codes:** ${errors.map((e) => `\`${e}\``).join(', ')}`, '');
  return lines.join('\n');
}

/** The API pages: an overview, one page per tag, and the schemas. */
export function apiPages(doc: OpenAPI): { pages: Page[]; sidebar: { text: string; link: string }[] } {
  const byTag = new Map<string, string[]>();
  for (const path of Object.keys(doc.paths).sort()) {
    const item = doc.paths[path]!;
    for (const method of METHODS) {
      const op = item[method];
      if (!op) continue;
      const tag = op.tags?.[0] ?? 'other';
      if (!byTag.has(tag)) byTag.set(tag, []);
      byTag.get(tag)!.push(operationMd(method, path, op));
    }
  }
  const tags = [...byTag.keys()].sort();
  const sources = ['internal/apidoc', 'internal/authz/routes.go'];
  const pages: Page[] = tags.map((tag) => ({
    path: `api/${tag}.md`,
    content: page({ title: `API: ${tag}`, body: escapeTags(byTag.get(tag)!.join('\n')), sources }),
  }));

  const schemaMd: string[] = [];
  for (const name of Object.keys(doc.components.schemas).sort()) {
    const s = doc.components.schemas[name]!;
    schemaMd.push(`### ${name} {#${schemaAnchor(name)}}`, '', body(s), '');
  }
  pages.push({ path: 'api/schemas.md', content: page({ title: 'API: schemas', body: escapeTags(schemaMd.join('\n')), sources }) });

  const count = [...byTag.values()].reduce((n, ops) => n + ops.length, 0);
  const overview = [
    `The hub's HTTP API, version \`${doc.info.version}\`: ${count} operations. It's generated from the route table, so every route the hub serves is here. A running hub serves the same document at \`GET /api/openapi.json\`, and this site has [a copy](/openapi.json).`,
    '',
    '## Calling it',
    '',
    '- **Access** is the least a caller needs: a role (`viewer`, `operator`, `admin`, `owner`), `signed_in`, or `public`. `node_key` is for chadash-node and `signature` for signed webhooks.',
    '- **A session** is the browser\'s `chadash_session` cookie. Unsafe requests must also come from the same origin.',
    '- **An API token** goes in `Authorization: Bearer cdt_…`, and only on routes with an API token scope. "Session only" routes refuse tokens.',
    '- **Request bodies are JSON** (`Content-Type: application/json`), and unknown fields are refused.',
    '- **Errors** answer `{"error": {"code", "message", "id", "args"}}`. Switch on `code`; `message` is English for people.',
    '',
    '## Areas',
    '',
    '| Area | Operations |',
    '|---|---|',
    ...tags.map((t) => `| [${t}](/api/${t}) | ${byTag.get(t)!.length} |`),
    `| [Schemas](/api/schemas) | ${Object.keys(doc.components.schemas).length} types |`,
    '',
  ].join('\n');
  pages.push({ path: 'api/index.md', content: page({ title: 'API', body: escapeTags(overview), sources }) });

  return {
    pages,
    sidebar: [...tags.map((t) => ({ text: t, link: `/api/${t}` })), { text: 'Schemas', link: '/api/schemas' }],
  };
}
