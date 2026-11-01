import { expect, test } from 'bun:test';
import { type OpenAPI, apiPages, schemaAnchor, typeOf } from '../scripts/api.ts';

const doc: OpenAPI = {
  info: { title: 'Cha Dash', version: '0.4.0-dev' },
  paths: {
    '/api/tokens': {
      get: {
        operationId: 'get_api_tokens',
        summary: 'API tokens.',
        tags: ['tokens'],
        responses: {
          '200': {
            content: {
              'application/json': {
                schema: { type: 'object', required: ['tokens'], properties: { tokens: { type: 'array', items: { $ref: '#/components/schemas/auth.APIToken' } } } },
              },
            },
          },
          default: { $ref: '#/components/responses/Error' },
        },
        'x-chadash-access': 'signed_in',
        'x-chadash-scope': '',
      },
    },
    '/api/nodes/{id}': {
      delete: {
        operationId: 'delete_api_nodes_id',
        summary: 'Revoke a node.',
        tags: ['nodes'],
        parameters: [{ name: 'id', in: 'path', required: true, schema: { type: 'string' } }],
        responses: { '204': { description: 'No Content' } },
        'x-chadash-access': 'admin',
        'x-chadash-scope': 'nodes.write',
        'x-chadash-errors': ['not_found'],
      },
    },
  },
  components: {
    schemas: {
      'auth.APIToken': {
        type: 'object',
        required: ['id'],
        properties: { id: { type: 'string' }, expires_at: { type: ['string', 'null'], format: 'date-time' } },
      },
    },
  },
};

test('typeOf names refs, arrays, unions and maps', () => {
  expect(typeOf({ $ref: '#/components/schemas/auth.APIToken' })).toBe('[auth.APIToken](/api/schemas#schema-auth-apitoken)');
  expect(typeOf({ type: 'array', items: { type: 'string' } })).toBe('string[]');
  expect(typeOf({ type: ['string', 'null'], format: 'date-time' })).toBe('string (date-time) \\| null');
  expect(typeOf({ type: 'object', additionalProperties: { type: 'integer' } })).toBe('map of integer');
  expect(typeOf(undefined)).toBe('any');
  expect(schemaAnchor('importer.importhttp.Role')).toBe('schema-importer-importhttp-role');
});

test('apiPages makes a page per tag, the schemas and an overview', () => {
  const { pages, sidebar } = apiPages(doc);
  expect(pages.map((p) => p.path).sort()).toEqual(['api/index.md', 'api/nodes.md', 'api/schemas.md', 'api/tokens.md']);
  expect(sidebar.map((s) => s.link)).toEqual(['/api/nodes', '/api/tokens', '/api/schemas']);

  const nodes = pages.find((p) => p.path === 'api/nodes.md')!.content;
  expect(nodes).toContain('### `DELETE /api/nodes/{id}` {#delete_api_nodes_id}');
  expect(nodes).toContain('**API token scope:** `nodes.write`');
  expect(nodes).toContain('| `id` | path | string | yes |  |');
  expect(nodes).toContain('**Error codes:** `not_found`');

  const tokens = pages.find((p) => p.path === 'api/tokens.md')!.content;
  expect(tokens).toContain('**Session only**');
  expect(tokens).toContain('| `tokens` | [auth.APIToken](/api/schemas#schema-auth-apitoken)[] | yes |  |');

  const schemas = pages.find((p) => p.path === 'api/schemas.md')!.content;
  expect(schemas).toContain('### auth.APIToken {#schema-auth-apitoken}');
  expect(schemas).toContain('| `expires_at` | string (date-time) \\| null |  |  |');

  const index = pages.find((p) => p.path === 'api/index.md')!.content;
  expect(index).toContain('2 operations');
  expect(index).toContain('[tokens](/api/tokens)');
});
