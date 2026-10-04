/**
 * The seven scenarios against each engine. These assertions record what we
 * observed on gridstack 14.0.0 as much as what we require of our engine:
 * if a gridstack release changes an answer, the test says so.
 */
import { beforeAll, describe, expect, test } from 'bun:test';
import { ENGINES } from '../harness/engines.ts';
import { layoutKey } from '../harness/checks.ts';
import { problemsOf, runAll, type ScenarioResult } from '../harness/run.ts';

const results: Record<string, Record<string, ScenarioResult>> = {};

beforeAll(() => {
  for (const e of ENGINES) results[e.key] = Object.fromEntries(runAll(e.make).map((r) => [r.scenario.id, r]));
});

const step = (engine: string, scenario: string, label: string) => {
  const s = results[engine]![scenario]!.steps.find((x) => x.label === label);
  if (!s) throw new Error(`no step ${label}`);
  return s;
};
const findings = (engine: string, scenario: string) => results[engine]![scenario]!.findings;

describe('every engine', () => {
  test('starts from the same front page', () => {
    const ours = layoutKey(step('chadash', 'front-page', 'load').snapshot);
    for (const e of ENGINES) expect(layoutKey(step(e.key, 'front-page', 'load').snapshot)).toBe(ours);
  });

  test('never overlaps widgets or sections, and never reaches outside a section', () => {
    for (const e of ENGINES) {
      for (const r of Object.values(results[e.key]!)) {
        const p = problemsOf(r);
        expect({ engine: e.key, scenario: r.scenario.id, overlaps: p.overlaps, outOfBounds: p.outOfBounds, overflow: p.overflow }).toEqual({
          engine: e.key,
          scenario: r.scenario.id,
          overlaps: 0,
          outOfBounds: 0,
          overflow: 0,
        });
      }
    }
  });

  test('undoes ten operations exactly (gridstack through save()/load() snapshots kept by the app)', () => {
    for (const e of ENGINES) {
      expect(findings(e.key, 'undo-10').undoExact).toBe('10/10');
      expect(findings(e.key, 'undo-10').backToStart).toBe(true);
    }
  });
});

describe('@chadash/layout', () => {
  test('every state uses supported sizes only', () => {
    for (const r of Object.values(results.chadash!)) {
      expect(problemsOf(r).unsupported).toBe(0);
      expect(problemsOf(r).offSystem).toBe(0);
    }
  });

  test('a drag across a full section keeps it packed', () => {
    expect(step('chadash', 'drag-large', 'move fleet → infra (4,0)').problems.gaps).toBe(0);
  });

  test('derivation is a function of the canonical layout', () => {
    expect(findings('chadash', 'derive')).toEqual({
      '12→4 equals 12→8→4': true,
      '12→4→12 restores desktop': true,
      'phone edit leaves desktop alone': true,
    });
  });

  test('a phone-only tweak survives serialize → restore, byte for byte', () => {
    expect(findings('chadash', 'round-trip')).toMatchObject({ reserializeIdentical: true, 'same at 4 columns': true, 'same at 12 columns': true });
  });
});

describe('gridstack 14, default (top gravity, moveScale columns)', () => {
  test('a drag across a full section opens a hole: 32 empty cells, the section grows from 6 to 10 rows', () => {
    const s = step('gs-top', 'drag-large', 'move fleet → infra (4,0)');
    expect(s.problems.gaps).toBe(32);
    expect(s.snapshot.sections.find((x) => x.id === 'infra')!.rows).toBe(10);
  });

  test('8 and 4 columns scale widgets to sizes outside the size system (3×4, 3×2, 2×4 ...)', () => {
    expect(step('gs-top', 'derive', '8 columns').problems.offSystem).toBe(3);
    expect(step('gs-top', 'derive', '4 columns').problems.offSystem).toBe(6);
  });

  test('derivation depends on the path: 12→4 is not 12→8→4', () => {
    expect(findings('gs-top', 'derive')['12→4 equals 12→8→4']).toBe(false);
  });
});

describe('gridstack 14, top gravity + list columns', () => {
  test('keeps sizes when columns change', () => {
    expect(problemsOf(results['gs-top-list']!.derive!).unsupported).toBe(0);
  });

  test('the same drag hole as the default', () => {
    expect(step('gs-top-list', 'drag-large', 'move fleet → infra (4,0)').problems.gaps).toBe(32);
  });

  test('restoring a saved layout and saving again gives a different string', () => {
    expect(findings('gs-top-list', 'round-trip').reserializeIdentical).toBe(false);
  });
});

describe('gridstack 14, compact (v14) inside sections, list for sections', () => {
  test('matches our engine on the drag, the resize and both inserts into a section', () => {
    for (const [scenario, label] of [
      ['drag-large', 'move fleet → infra (4,0)'],
      ['resize-next-to-full-row', 'resize plex → large'],
      ['insert-full', 'insert pihole-2 at infra (4,2)'],
      ['derive', '8 columns'],
      ['derive', '4 columns'],
    ] as const) {
      expect(layoutKey(step('gs-compact', scenario, label).snapshot)).toBe(layoutKey(step('chadash', scenario, label).snapshot));
    }
  });

  test('a widget added to a section reshuffles the sections: Media jumps beside Home, Activity below it', () => {
    const snap = step('gs-compact', 'insert-full', 'insert pihole').snapshot;
    const at = (id: string) => snap.sections.find((s) => s.id === id)!;
    expect([at('media').x, at('media').y]).toEqual([4, 6]);
    expect([at('activity').x, at('activity').y]).toEqual([0, 8]);
    // ours: both drop by the two rows Home grew, and keep their columns
    const ours = step('chadash', 'insert-full', 'insert pihole').snapshot;
    expect(ours.sections.map((s) => [s.id, s.x, s.y])).toEqual([['home', 0, 0], ['infra', 4, 0], ['media', 0, 8], ['activity', 8, 8]]);
  });

  test('order comes from positions: resizing a back-filled Small moves the Large tile before it', () => {
    const label = 'resize containers → wide';
    const fleet = (k: string) => step(k, 'undo-10', label).snapshot.sections.find((s) => s.id === 'infra')!.items.find((i) => i.id === 'fleet')!;
    expect([fleet('chadash').x, fleet('chadash').y]).toEqual([0, 2]); // stays where it was
    expect([fleet('gs-compact').x, fleet('gs-compact').y]).toEqual([4, 2]); // pushed to the other side
  });

  test('a phone edit rewrites the desktop layout (7 desktop widgets move)', () => {
    expect(findings('gs-compact', 'derive')['phone edit leaves desktop alone']).toBe(false);
    expect(findings('gs-compact', 'derive')['desktop widgets moved by a phone edit']).toBe(7);
  });
});
