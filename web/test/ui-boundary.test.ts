/**
 * The line ADR-0032 draws: buttons, the ×, fields and dialogs come from
 * src/ui, and feature code doesn't write them by hand. A hand-written copy
 * is how the danger button ended up defined three times.
 */
import { describe, expect, it } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';

const SRC = join(import.meta.dirname, '..', 'src');
const walk = (dir: string): string[] => readdirSync(dir, { withFileTypes: true }).flatMap((e) => (e.isDirectory() ? walk(join(dir, e.name)) : [join(dir, e.name)]));

/** Feature files allowed one pattern, with the reason. Keep this short. */
const ALLOWED: Record<string, readonly string[]> = {
  // Written before UiDialog by a change still in flight; move it over next.
  'widgets/HomeAssistantPanel.vue': ['dialog', 'button', 'close'],
};

const RULES: { name: string; re: RegExp; use: string }[] = [
  { name: 'button', re: /<(button|a)\b[^>]*\sclass="btn\b/, use: 'UiButton' },
  { name: 'button', re: /:class="\[\s*'btn'/, use: 'UiButton' },
  { name: 'close', re: /<button\b[^>]*\sclass="x"/, use: 'UiCloseButton' },
  { name: 'field', re: /<(div|label)\b[^>]*\sclass="field\b/, use: 'UiField' },
  { name: 'dialog', re: /<dialog\b/, use: 'UiDialog' },
];

function templateOf(text: string): string {
  const start = text.indexOf('<template>');
  return start < 0 ? '' : text.slice(start, text.lastIndexOf('</template>'));
}

describe('UI primitives', () => {
  const files = walk(SRC)
    .filter((f) => f.endsWith('.vue'))
    .map((f) => f.slice(SRC.length + 1))
    .filter((f) => !f.startsWith('ui/'));

  it('feature templates use them instead of hand-written markup', () => {
    const bad: string[] = [];
    for (const file of files) {
      const tpl = templateOf(readFileSync(join(SRC, file), 'utf8'));
      for (const rule of RULES) {
        if (ALLOWED[file]?.includes(rule.name)) continue;
        // [^>]* spans lines, so a tag split over several lines is caught too.
        for (const m of tpl.matchAll(new RegExp(rule.re.source, 'g'))) {
          bad.push(`${file} (template line ${tpl.slice(0, m.index).split('\n').length}): use ${rule.use}`);
        }
      }
    }
    expect(bad).toEqual([]);
  });

  it('the danger button is defined once, in ui.css', () => {
    const where = walk(SRC)
      .filter((f) => /\.(css|vue)$/.test(f))
      .filter((f) => /\.btn\.danger\b[^{]*\{/.test(readFileSync(f, 'utf8')))
      .map((f) => f.slice(SRC.length + 1));
    expect(where).toEqual(['ui/ui.css']);
  });
});
