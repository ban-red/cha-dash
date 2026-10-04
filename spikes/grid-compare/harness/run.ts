/**
 * Replays the scenarios against an adapter and checks every state.
 */
import { DOGFOOD, PIHOLE, SCENARIOS, type BoardData, type Scenario, type Step } from '../scenarios/index.ts';
import { checkSnapshot, layoutKey, moved, total, widgetsById, type Problems } from './checks.ts';
import type { GridAdapter, Snapshot } from './types.ts';

export type AdapterFactory = () => GridAdapter;

export interface StepResult {
  readonly label: string;
  readonly snapshot: Snapshot;
  readonly problems: Problems;
  /** Widgets whose cell or size changed compared to the previous state at the same column count. */
  readonly moved: readonly string[];
  readonly error?: string;
}

export interface ScenarioResult {
  readonly scenario: Scenario;
  readonly engine: string;
  readonly steps: readonly StepResult[];
  /** Scenario-specific findings (undo exactness, path independence, ...). */
  readonly findings: Record<string, string | number | boolean>;
}

const CATALOG = widgetsById(DOGFOOD, [PIHOLE, { ...PIHOLE, id: 'pihole-2' }]);

function label(step: Step): string {
  switch (step.op) {
    case 'move':
      return `move ${step.item} → ${step.to.section} (${step.to.x},${step.to.y})`;
    case 'resize':
      return `resize ${step.item} → ${step.size}`;
    case 'insert':
      return `insert ${step.item.id}${step.near ? ` at ${step.near.section} (${step.near.x},${step.near.y})` : ''}`;
    case 'columns':
      return `${step.columns} columns`;
    case 'undo':
      return `undo ×${step.times}`;
    case 'roundTrip':
      return 'serialize → restore';
  }
}

function apply(a: GridAdapter, step: Step): void {
  switch (step.op) {
    case 'move':
      return a.move(step.item, step.to);
    case 'resize':
      return a.resize(step.item, step.size);
    case 'insert':
      return a.insert(step.item, step.near);
    case 'columns':
      return a.setColumns(step.columns);
    case 'undo':
    case 'roundTrip':
      return; // handled by the runner
  }
}

export function runScenario(make: AdapterFactory, scenario: Scenario, board: BoardData = DOGFOOD): ScenarioResult {
  const a = make();
  const findings: Record<string, string | number | boolean> = {};
  const steps: StepResult[] = [];
  const record = (lbl: string, prevSnap?: Snapshot, error?: string): Snapshot => {
    const snapshot = a.snapshot();
    const prev = prevSnap ?? [...steps].reverse().find((s) => s.snapshot.columns === snapshot.columns)?.snapshot;
    steps.push({
      label: lbl,
      snapshot,
      problems: checkSnapshot(snapshot, CATALOG),
      moved: prev ? moved(prev, snapshot) : [],
      ...(error ? { error } : {}),
    });
    return snapshot;
  };

  a.load(board);
  const start = record('load');
  const before: Snapshot[] = [];

  for (const step of scenario.steps) {
    if (step.op === 'undo') {
      let exact = 0;
      const steps = before.length;
      for (let i = 0; i < steps; i++) {
        a.undo();
        const want = before.pop();
        if (want && layoutKey(a.snapshot()) === layoutKey(want)) exact++;
      }
      findings.undoExact = `${exact}/${steps}`;
      findings.backToStart = layoutKey(a.snapshot()) === layoutKey(start);
      record(label(step));
      continue;
    }
    if (step.op === 'roundTrip') {
      roundTrip(make, a, findings);
      record(label(step));
      continue;
    }
    const prior = a.snapshot();
    try {
      apply(a, step);
      const after = record(label(step));
      // an edit that changes nothing leaves no undo step (a drop back in place)
      if (step.op !== 'columns' && layoutKey(after) !== layoutKey(prior)) before.push(prior);
      else if (step.op !== 'columns') findings.noOps = Number(findings.noOps ?? 0) + 1;
    } catch (e) {
      record(label(step), undefined, e instanceof Error ? e.message : String(e));
    }
  }

  if (scenario.id === 'derive') deriveFindings(make, board, steps, start, findings);
  a.dispose();
  return { scenario, engine: a.name, steps, findings };
}

/** serialize → restore into a fresh instance → compare, at the current column count and at 12. */
function roundTrip(make: AdapterFactory, a: GridAdapter, findings: Record<string, string | number | boolean>): void {
  const columns = a.snapshot().columns;
  const json = a.serialize();
  const b = make();
  b.restore(json);
  findings.reserializeIdentical = b.serialize() === json;
  b.setColumns(columns);
  findings[`same at ${columns} columns`] = layoutKey(b.snapshot()) === layoutKey(a.snapshot());
  const lostAt = moved(a.snapshot(), b.snapshot());
  if (lostAt.length) findings[`changed at ${columns}`] = lostAt.join(', ');
  a.setColumns(12);
  b.setColumns(12);
  findings['same at 12 columns'] = layoutKey(b.snapshot()) === layoutKey(a.snapshot());
  findings.bytes = json.length;
  b.dispose();
}

/** Is derivation a function of the canonical layout, or of the path taken? */
function deriveFindings(make: AdapterFactory, board: BoardData, steps: StepResult[], start: Snapshot, findings: Record<string, string | number | boolean>): void {
  const at4ViaEight = steps.find((s) => s.label === '4 columns')!.snapshot;
  const direct = make();
  direct.load(board);
  direct.setColumns(4);
  findings['12→4 equals 12→8→4'] = layoutKey(direct.snapshot()) === layoutKey(at4ViaEight);
  direct.setColumns(12);
  findings['12→4→12 restores desktop'] = layoutKey(direct.snapshot()) === layoutKey(start);
  direct.dispose();

  // a phone-only edit must not change the desktop layout
  const edit = make();
  edit.load(board);
  edit.setColumns(4);
  edit.move('uptimekuma', { section: 'infra', x: 0, y: 0 });
  edit.setColumns(12);
  const changed = moved(start, edit.snapshot());
  findings['phone edit leaves desktop alone'] = changed.length === 0;
  if (changed.length) findings['desktop widgets moved by a phone edit'] = changed.length;
  edit.dispose();
}

export function runAll(make: AdapterFactory): ScenarioResult[] {
  return SCENARIOS.map((s) => runScenario(make, s));
}

export function problemsOf(r: ScenarioResult): Problems {
  const out = { overlaps: 0, outOfBounds: 0, gaps: 0, unsupported: 0, offSystem: 0, overflow: 0 };
  for (const s of r.steps) for (const k of Object.keys(out) as (keyof Problems)[]) out[k] += s.problems[k];
  return out;
}

export { total };
