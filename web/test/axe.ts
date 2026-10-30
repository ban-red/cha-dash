/**
 * axe-core over a rendered view, held to WCAG 2.2 A and AA.
 *
 * happy-dom has no layout or paint, so axe can't measure colour contrast
 * here: that's checked on the tokens instead (design/tokens
 * test/contrast.test.ts for every token pair, test/token-pairs.test.ts for
 * the pairs the components declare). Everything else axe checks for WCAG A
 * and AA runs: names, roles, ARIA, labels, lists, landmarks' content, ids.
 */
import axe from 'axe-core';

const WCAG_TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22a', 'wcag22aa', 'best-practice'];

/** What index.html gives every page. */
function documentBasics(): void {
  document.documentElement.lang = 'en';
  if (!document.title) document.title = 'Cha Dash';
}

/**
 * Readable violations under root: "rule (impact): help — target: summary". Empty means clean.
 * skip names rules that don't apply to a part tested alone, such as region for one the page puts in a landmark.
 */
export async function axeViolations(root: Element = document.body, skip: readonly string[] = []): Promise<string[]> {
  documentBasics();
  const res = await axe.run(root, {
    runOnly: { type: 'tag', values: WCAG_TAGS },
    rules: Object.fromEntries(['color-contrast', ...skip].map((r) => [r, { enabled: false }])),
    resultTypes: ['violations'],
  });
  return res.violations.flatMap((v) =>
    v.nodes.map((n) => `${v.id} (${v.impact ?? 'n/a'}): ${v.help} — ${n.target.join(' ')}: ${(n.failureSummary ?? '').replace(/\s+/g, ' ').trim()}`),
  );
}
