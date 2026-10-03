import { describe, expect, test } from 'bun:test';
import {
  LAYOUT_VERSION,
  LayoutError,
  createBoard,
  deserialize,
  fromDocument,
  resize,
  serialize,
  toDocument,
  type LayoutErrorCode,
} from '../src/index.ts';
import { item, positions, section } from './helpers.ts';

function expectCode(fn: () => unknown, code: LayoutErrorCode, message?: string): void {
  let caught: unknown;
  try {
    fn();
  } catch (error) {
    caught = error;
  }
  expect(caught).toBeInstanceOf(LayoutError);
  expect((caught as LayoutError).code).toBe(code);
  if (message !== undefined) expect((caught as LayoutError).message).toContain(message);
}

const sample = () =>
  createBoard({
    sections: [
      section('infra', [item('pve', 'large', ['wide', 'large']), item('clock', 'small')], 8, {
        title: 'Infrastructure',
        overrides: { phone: { order: ['clock', 'pve'] } },
      }),
      section('media', [item('cams', 'hero', ['hero', 'large'], 'camera')], 12),
    ],
  });

describe('serialization', () => {
  test('writes a versioned, readable document', () => {
    expect(toDocument(sample())).toEqual({
      version: 1,
      columns: 12,
      sections: [
        {
          id: 'infra',
          title: 'Infrastructure',
          span: 8,
          items: [
            { id: 'pve', type: 'test.widget', size: 'large', supportedSizes: ['wide', 'large'], x: 0, y: 0 },
            { id: 'clock', type: 'test.widget', size: 'small', supportedSizes: ['small'], x: 4, y: 0 },
          ],
          overrides: { phone: { order: ['clock', 'pve'] } },
        },
        {
          id: 'media',
          span: 12,
          items: [{ id: 'cams', type: 'camera', size: 'hero', supportedSizes: ['hero', 'large'], x: 0, y: 0 }],
        },
      ],
    });
    expect(LAYOUT_VERSION).toBe(1);
  });

  test('round-trips exactly', () => {
    const board = sample();
    const json = serialize(board);
    expect(deserialize(json)).toEqual(board);
    expect(serialize(deserialize(json))).toBe(json);
    expect(serialize(board, { space: 2 })).toContain('\n  "version": 1');
  });

  test('equal boards serialize identically regardless of edit history', () => {
    const base = createBoard({
      sections: [section('s', [item('a', 'large', ['large', 'wide']), item('b', 'large', ['large', 'wide'])])],
    });
    const ab = resize(resize(base, 'a', 'wide', { breakpoint: 'phone' }), 'b', 'wide', { breakpoint: 'phone' });
    const ba = resize(resize(base, 'b', 'wide', { breakpoint: 'phone' }), 'a', 'wide', { breakpoint: 'phone' });
    expect(serialize(ab)).toBe(serialize(ba));
  });

  test('positions are recomputed from order on load', () => {
    const doc = JSON.parse(serialize(sample()));
    doc.sections[0].items[1].x = 99;
    expect(positions(fromDocument(doc), 'infra')).toEqual(['pve@0,0', 'clock@4,0']);
  });

  test('rejects malformed documents', () => {
    expectCode(() => deserialize('{nope'), 'INVALID_DOCUMENT', 'not valid JSON');
    expectCode(() => fromDocument(null), 'INVALID_DOCUMENT');
    expectCode(() => fromDocument([]), 'INVALID_DOCUMENT');
    expectCode(() => fromDocument({ columns: 12, sections: [] }), 'INVALID_DOCUMENT', 'version');
    expectCode(() => fromDocument({ version: '1', columns: 12, sections: [] }), 'INVALID_DOCUMENT', 'version');
    expectCode(() => fromDocument({ version: 1, columns: 16, sections: [] }), 'INVALID_DOCUMENT', 'columns');
    expectCode(() => fromDocument({ version: 1, columns: 12 }), 'INVALID_DOCUMENT', 'sections');
    expectCode(() => fromDocument({ version: 1, columns: 12, sections: [42] }), 'INVALID_DOCUMENT', 'sections[0]');
    expectCode(() => fromDocument({ version: 1, columns: 12, sections: [{ id: 's', span: 12 }] }), 'INVALID_DOCUMENT', 'items');
    expectCode(() => fromDocument({ version: 1, columns: 12, sections: [{ id: 's', items: [] }] }), 'INVALID_DOCUMENT', 'span');
    expectCode(
      () => fromDocument({ version: 1, columns: 12, sections: [{ id: 's', span: 12, items: [null] }] }),
      'INVALID_DOCUMENT',
      'items[0]',
    );
    expectCode(
      () =>
        fromDocument({
          version: 1,
          columns: 12,
          sections: [{ id: 's', span: 4, items: [{ id: 'h', type: 't', size: 'hero', supportedSizes: ['hero'] }] }],
        }),
      'INVALID_DOCUMENT',
      'does not fit',
    );
  });

  test('refuses documents from the future', () => {
    expectCode(() => fromDocument({ version: 2, columns: 12, sections: [] }), 'UNSUPPORTED_VERSION', 'newer');
  });

  test('migrates older documents through the migration hook', () => {
    // A hypothetical pre-release format: flat widgets with a "section" field.
    const v0 = {
      version: 0,
      widgets: [
        { id: 'a', kind: 'clock', section: 'main', size: 'small' },
        { id: 'b', kind: 'sonarr', section: 'main', size: 'wide' },
      ],
    };
    expectCode(() => fromDocument(v0), 'UNSUPPORTED_VERSION', 'no migration from layout document version 0');
    const migrations = {
      0: (doc: Record<string, unknown>) => ({
        version: 1,
        columns: 12,
        sections: [
          {
            id: 'main',
            span: 12,
            items: (doc['widgets'] as { id: string; kind: string; size: string }[]).map((w) => ({
              id: w.id,
              type: w.kind,
              size: w.size,
              supportedSizes: [w.size],
            })),
          },
        ],
      }),
    };
    const board = fromDocument(v0, { migrations });
    expect(positions(board, 'main')).toEqual(['a@0,0', 'b@2,0']);
    expect(deserialize(JSON.stringify(v0), { migrations })).toEqual(board);
  });

  test('a migration must advance the version by exactly one', () => {
    expectCode(
      () => fromDocument({ version: 0 }, { migrations: { 0: () => ({ version: 0 }) } }),
      'INVALID_DOCUMENT',
      'must return a document with version 1',
    );
  });
});
