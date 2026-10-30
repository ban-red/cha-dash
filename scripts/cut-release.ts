#!/usr/bin/env bun
/**
 * `bun run release:cut <version>`: moves what's under "## [Unreleased]" in
 * CHANGELOG.md into the version's section, the last commit before the tag.
 *
 * A new version gets a section dated today, opened by a TODO(summary) line:
 * write the release's summary there (what it is, and what to do on upgrade).
 * scripts/release-notes.sh, and so the Release workflow, refuses notes that
 * still have it. A version that already has a section (entries were added after
 * it was cut) gets them merged in, each under its own heading. Either way the
 * [Unreleased] link compares from the new tag and the version gets its link.
 *
 *   bun run release:cut 0.2.0-beta.3                 a leading v is fine too
 *   bun run release:cut 0.2.0-beta.3 --date 2026-11-02
 *
 * It only edits the file: read the diff, write the summary, commit, then tag
 * that commit. Anything committed after it is the next release's.
 */
import { dirname, join } from 'node:path';

export const SUMMARY_TODO = 'TODO(summary): what this release is, and what to do on upgrade.';
const ORDER = ['Added', 'Changed', 'Deprecated', 'Removed', 'Fixed', 'Security'];

/** The entries of one changelog section, by heading, each entry with its continuation lines. */
function parse(body: string): Map<string, string[]> {
  const out = new Map<string, string[]>();
  let heading = '';
  for (const line of body.split('\n')) {
    const h = line.match(/^### (.+)$/);
    if (h) {
      heading = h[1]!.trim();
      if (!out.has(heading)) out.set(heading, []);
    } else if (line.startsWith('- ')) {
      if (!heading) throw new Error(`An entry under Unreleased has no ### heading: ${line.slice(0, 60)}`);
      out.get(heading)!.push(line);
    } else if (line.trim() !== '' && heading && out.get(heading)!.length) {
      const list = out.get(heading)!;
      list[list.length - 1] += '\n' + line; // a wrapped entry
    }
  }
  for (const [k, v] of out) if (!v.length) out.delete(k);
  return out;
}

const sorted = (m: Map<string, string[]>) =>
  [...m.keys()].sort((a, b) => (ORDER.indexOf(a) + 1 || 99) - (ORDER.indexOf(b) + 1 || 99));

export interface Cut {
  text: string;
  moved: Record<string, number>;
  created: boolean;
}

/** Moves Unreleased into version's section (made, or merged into) and updates the links. */
export function cut(changelog: string, version: string, date: string): Cut {
  const v = version.replace(/^v/, '');
  if (!/^\d+\.\d+\.\d+(-(beta|rc)\.\d+)?$/.test(v)) throw new Error(`Not a version: ${version} (MAJOR.MINOR.PATCH, optionally -beta.N or -rc.N)`);
  const head = '## [Unreleased]\n';
  const u = changelog.indexOf(head);
  if (u < 0) throw new Error('No "## [Unreleased]" heading');
  const after = u + head.length;
  const next = changelog.indexOf('\n## [', after);
  if (next < 0) throw new Error('No version section after Unreleased');
  const entries = parse(changelog.slice(after, next));
  if (!entries.size) throw new Error('Nothing under Unreleased to move');
  const moved = Object.fromEntries([...entries].map(([k, list]) => [k, list.length]));

  // Unreleased is left empty.
  let text = changelog.slice(0, after) + '\n' + changelog.slice(next + 1);
  const mine = `\n## [${v}]`;
  let at = text.indexOf(mine);
  const created = at < 0;
  if (created) {
    const blocks = sorted(entries).map((k) => `### ${k}\n\n${entries.get(k)!.join('\n')}\n`);
    const section = `## [${v}] - ${date}\n\n${SUMMARY_TODO}\n\n${blocks.join('\n')}\n`;
    const insert = text.indexOf(head) + head.length + 1;
    text = text.slice(0, insert) + section + text.slice(insert);
  } else {
    at += 1;
    for (const k of sorted(entries)) {
      const end = (() => {
        const n = text.indexOf('\n## [', at + 1);
        const links = text.search(/\n\[[^\]]+\]: /);
        return Math.min(n < 0 ? text.length : n, links < 0 ? text.length : links);
      })();
      const section = text.slice(at, end);
      const lines = entries.get(k)!.join('\n') + '\n';
      const h = section.indexOf(`\n### ${k}\n`);
      if (h >= 0) {
        // Newest first, as when they were written: right under the heading.
        const pos = at + h + `\n### ${k}\n`.length + 1;
        text = text.slice(0, pos) + lines + text.slice(pos);
      } else {
        // A heading the section lacks goes before the first one that sorts after it.
        const later = ORDER.slice(ORDER.indexOf(k) + 1)
          .map((x) => section.indexOf(`\n### ${x}\n`))
          .filter((i) => i >= 0);
        const pos = later.length ? at + Math.min(...later) + 1 : end + 1;
        text = text.slice(0, pos) + `### ${k}\n\n${lines}\n` + text.slice(pos);
      }
    }
  }

  // [Unreleased] compares from this tag; the version links to its release.
  const link = text.match(/^\[Unreleased\]: (https:\/\/github\.com\/[^/]+\/[^/]+)\/compare\/v[^.\s]+\.[^\s]*?\.\.\.HEAD$/m);
  if (!link) throw new Error('No [Unreleased]: …/compare/v….…HEAD link at the bottom');
  const repo = link[1]!;
  text = text.replace(link[0], `[Unreleased]: ${repo}/compare/v${v}...HEAD`);
  if (!new RegExp(`^\\[${v.replace(/\./g, '\\.')}\\]: `, 'm').test(text)) {
    text = text.replace(`[Unreleased]: ${repo}/compare/v${v}...HEAD`, `[Unreleased]: ${repo}/compare/v${v}...HEAD\n[${v}]: ${repo}/releases/tag/v${v}`);
  }
  return { text, moved, created };
}

if (import.meta.main) {
  const argv = process.argv.slice(2);
  if (!argv.length || argv.includes('--help') || argv.includes('-h')) {
    const doc = (await Bun.file(import.meta.path).text()).match(/\/\*\*([\s\S]*?)\*\//)?.[1] ?? '';
    console.log(doc.replace(/^ \* ?/gm, '').trim());
    process.exit(argv.length ? 0 : 2);
  }
  const d = argv.indexOf('--date');
  const now = new Date();
  const today = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}`;
  const date = d >= 0 ? argv[d + 1]! : today;
  const file = join(dirname(import.meta.dir), 'CHANGELOG.md');
  try {
    const r = cut(await Bun.file(file).text(), argv[0]!, date);
    await Bun.write(file, r.text);
    const what = Object.entries(r.moved).map(([k, n]) => `${n} ${k}`).join(', ');
    console.log(`${r.created ? 'Opened' : 'Merged into'} ${argv[0]!.replace(/^v/, '')}: ${what}.`);
    if (r.text.includes(SUMMARY_TODO)) console.log(`Write the summary where CHANGELOG.md says ${SUMMARY_TODO.split(':')[0]}, then commit.`);
  } catch (e) {
    console.error(`release:cut: ${(e as Error).message}`);
    process.exit(1);
  }
}
