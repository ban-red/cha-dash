import { describe, expect, test } from 'bun:test';
import { cut, SUMMARY_TODO } from './cut-release';

const REPO = 'https://github.com/ban-red/cha-dash';
const log = (unreleased: string, current = '') => `# Changelog

Intro.

## [Unreleased]

${unreleased}
${current}## [0.1.0] - 2026-01-01

The first.

### Added

- **Old.** It was.

[Unreleased]: ${REPO}/compare/v0.1.0...HEAD
[0.1.0]: ${REPO}/releases/tag/v0.1.0
`;

describe('release:cut', () => {
  test('opens a new section with the summary to write, and moves the links on', () => {
    const r = cut(log('### Fixed\n\n- **B.** Fixed.\n\n### Added\n\n- **A.** New.\n- **A2.** Also\n  wrapped.\n'), 'v0.2.0', '2026-02-02');
    expect(r.created).toBe(true);
    expect(r.moved).toEqual({ Fixed: 1, Added: 2 });
    expect(r.text).toContain(
      `## [Unreleased]\n\n## [0.2.0] - 2026-02-02\n\n${SUMMARY_TODO}\n\n### Added\n\n- **A.** New.\n- **A2.** Also\n  wrapped.\n\n### Fixed\n\n- **B.** Fixed.\n\n## [0.1.0]`,
    );
    expect(r.text).toContain(`[Unreleased]: ${REPO}/compare/v0.2.0...HEAD\n[0.2.0]: ${REPO}/releases/tag/v0.2.0\n[0.1.0]:`);
  });

  test('merges into a section already cut, under its headings, adding the ones it lacks in order', () => {
    const current = `## [0.2.0] - 2026-02-02\n\nThe summary.\n\n### Added\n\n- **A.** New.\n\n### Security\n\n- **S.** Safe.\n\n`;
    const once = log('### Fixed\n\n- **F.** Late fix.\n\n### Added\n\n- **L.** Late.\n', current);
    const withLinks = once.replace(`compare/v0.1.0...HEAD`, `compare/v0.2.0...HEAD\n[0.2.0]: ${REPO}/releases/tag/v0.2.0`);
    const r = cut(withLinks, '0.2.0', '2026-03-03');
    expect(r.created).toBe(false);
    expect(r.text).toContain('## [Unreleased]\n\n## [0.2.0] - 2026-02-02\n\nThe summary.\n\n### Added\n\n- **L.** Late.\n- **A.** New.\n\n### Fixed\n\n- **F.** Late fix.\n\n### Security\n\n- **S.** Safe.\n\n## [0.1.0]');
    expect(r.text.match(/^\[0\.2\.0\]: /gm)).toHaveLength(1);
  });

  test('refuses what it can’t do right', () => {
    expect(() => cut(log(''), '0.2.0', '2026-02-02')).toThrow('Nothing under Unreleased');
    expect(() => cut(log('- **A.** No heading.\n'), '0.2.0', '2026-02-02')).toThrow('no ### heading');
    expect(() => cut(log('### Added\n\n- **A.**\n'), '0.2', '2026-02-02')).toThrow('Not a version');
  });
});
