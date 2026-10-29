import { describe, expect, it } from 'vitest';
import { applyPatch, getAt, parsePointer, PatchError, toPointer, type PatchOp } from '../src/lib/jsonpatch';

describe('JSON pointers', () => {
  it('parses and escapes per RFC 6901', () => {
    expect(parsePointer('')).toEqual([]);
    expect(parsePointer('/a~1b/c~0d/0')).toEqual(['a/b', 'c~d', '0']);
    expect(toPointer('series', 'pve:owl/qemu/301#mem.pct')).toBe('/series/pve:owl~1qemu~1301#mem.pct');
    expect(parsePointer(toPointer('~/', ''))).toEqual(['~/', '']);
    expect(() => parsePointer('a')).toThrow();
  });

  it('resolves the RFC 6901 examples', () => {
    const doc = { foo: ['bar', 'baz'], '': 0, 'a/b': 1, 'c%d': 2, 'm~n': 8 };
    expect(getAt(doc, '')).toBe(doc);
    expect(getAt(doc, '/foo/0')).toBe('bar');
    expect(getAt(doc, '/')).toBe(0);
    expect(getAt(doc, '/a~1b')).toBe(1);
    expect(getAt(doc, '/m~0n')).toBe(8);
  });
});

describe('applyPatch', () => {
  it('adds, replaces and removes object members (what the hub sends)', () => {
    const doc = { graph: { resources: { a: { id: 'a', state: 'running' } } as Record<string, unknown> } };
    applyPatch(doc, [
      { op: 'add', path: '/graph/resources/b', value: { id: 'b' } },
      { op: 'replace', path: '/graph/resources/a', value: { id: 'a', state: 'stopped' } },
    ]);
    expect(doc.graph.resources).toEqual({ a: { id: 'a', state: 'stopped' }, b: { id: 'b' } });
    applyPatch(doc, [{ op: 'remove', path: '/graph/resources/a' }]);
    expect(Object.keys(doc.graph.resources)).toEqual(['b']);
  });

  it('handles escaped keys such as series ids with slashes', () => {
    const doc = { metrics: { series: {} as Record<string, unknown> } };
    applyPatch(doc, [{ op: 'add', path: toPointer('metrics', 'series', 'pve:owl/qemu/301#cpu.pct'), value: { value: 3 } }]);
    expect(doc.metrics.series['pve:owl/qemu/301#cpu.pct']).toEqual({ value: 3 });
  });

  it('adds, replaces and removes fields inside a member (live-document diffs)', () => {
    const key = 'pve:owl/qemu/301#cpu.pct';
    const doc = {
      metrics: { series: { [key]: { value: 3, as_of: 't0', history: [1, 2, 3] } as Record<string, unknown> } },
    };
    const series = doc.metrics.series[key]!;
    const base = toPointer('metrics', 'series', key);
    applyPatch(doc, [
      { op: 'replace', path: `${base}/as_of`, value: 't1' },
      { op: 'add', path: `${base}/stale`, value: true },
      { op: 'replace', path: `${base}/value`, value: 4 },
    ]);
    expect(series).toEqual({ value: 4, as_of: 't1', stale: true, history: [1, 2, 3] });
    applyPatch(doc, [
      { op: 'replace', path: `${base}/history`, value: [2, 3, 4] },
      { op: 'remove', path: `${base}/stale` },
    ]);
    expect(series).toEqual({ value: 4, as_of: 't1', history: [2, 3, 4] });
    expect(doc.metrics.series[key]).toBe(series); // patched in place, not replaced
    // A field of a member that isn't there is an error, not a new member.
    expect(() => applyPatch(doc, [{ op: 'replace', path: toPointer('metrics', 'series', 'gone', 'value'), value: 1 }])).toThrow(PatchError);
  });

  it('works on arrays, including "-"', () => {
    const doc = { a: [1, 2, 3] };
    applyPatch(doc, [
      { op: 'add', path: '/a/1', value: 9 },
      { op: 'add', path: '/a/-', value: 4 },
      { op: 'remove', path: '/a/0' },
      { op: 'replace', path: '/a/0', value: 8 },
    ]);
    expect(doc.a).toEqual([8, 2, 3, 4]);
  });

  it('implements move, copy and test', () => {
    const doc = { a: { x: 1 }, b: {} as Record<string, unknown> };
    applyPatch(doc, [
      { op: 'copy', from: '/a', path: '/b/copy' },
      { op: 'move', from: '/a/x', path: '/b/x' },
      { op: 'test', path: '/b/copy', value: { x: 1 } },
    ]);
    expect(doc).toEqual({ a: {}, b: { copy: { x: 1 }, x: 1 } });
    // a copy is a deep copy
    (doc.b['copy'] as { x: number }).x = 5;
    expect(applyPatch(doc, [{ op: 'test', path: '/b/x', value: 1 }])).toBe(doc);
  });

  it('replaces the root', () => {
    expect(applyPatch({ a: 1 }, [{ op: 'replace', path: '', value: { b: 2 } }])).toEqual({ b: 2 });
  });

  it('refuses what RFC 6902 calls errors, naming the operation', () => {
    const cases: PatchOp[][] = [
      [{ op: 'replace', path: '/missing', value: 1 }],
      [{ op: 'remove', path: '/missing' }],
      [{ op: 'add', path: '/no/parent', value: 1 }],
      [{ op: 'add', path: '/a/9', value: 1 }],
      [{ op: 'add', path: '/a/01', value: 1 }],
      [{ op: 'test', path: '/a/0', value: 2 }],
      [{ op: 'move', from: '/o', path: '/o/inner' }],
    ];
    for (const patch of cases) {
      const doc = { a: [1], o: {} };
      expect(() => applyPatch(doc, patch), JSON.stringify(patch)).toThrow(PatchError);
    }
    try {
      applyPatch({ a: 1 }, [
        { op: 'replace', path: '/a', value: 2 },
        { op: 'remove', path: '/b' },
      ]);
    } catch (err) {
      expect(err).toBeInstanceOf(PatchError);
      expect((err as PatchError).index).toBe(1);
    }
  });
});
