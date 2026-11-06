/**
 * Visual regression (E5): the widget sheets in test/visual/, in headless
 * Chromium through Playwright. `bun run test:visual` in web/.
 *
 * Baselines are made only in CI. Fonts rasterise differently on each OS, so a
 * reference is only meaningful on the platform that made it, and the
 * committed ones are CI's (Linux, Chromium):
 *
 *   CI on Linux   test/visual/__screenshots__/<widget>/<theme>.png, committed.
 *                 The Visual job compares with them, or records them with
 *                 --update and uploads them as the visual-baselines artifact.
 *   anywhere else test/visual/.local/<platform>/…, git-ignored. The first run
 *                 records them (update mode, below); later runs compare with
 *                 them, so a local run still catches a change you made.
 *
 * Diffs (reference, actual, diff) go to test/visual/.diffs/, git-ignored;
 * CI uploads that folder when a comparison fails.
 */
import { existsSync } from 'node:fs';
import { join } from 'node:path';
import vue from '@vitejs/plugin-vue';
import { playwright } from '@vitest/browser-playwright';
import { defineConfig } from 'vitest/config';

const here = import.meta.dirname;
const visual = join(here, 'test/visual');
const committed = !!process.env['CI'] && process.platform === 'linux';
const references = committed ? join(visual, '__screenshots__') : join(visual, '.local', process.platform);

export default defineConfig({
  plugins: [vue()],
  resolve: { dedupe: ['vue'] },
  test: {
    include: ['test/visual/**/*.visual.test.ts'],
    restoreMocks: true,
    // The copies Vitest attaches to a failed comparison: beside the diffs, not in .vitest/.
    attachmentsDir: 'test/visual/.diffs/attachments',
    testTimeout: 120_000,
    // Locally, a first run records the references instead of failing on every sheet.
    update: !committed && !existsSync(references) ? true : undefined,
    browser: {
      enabled: true,
      headless: true,
      provider: playwright({
        contextOptions: { locale: 'en-US', timezoneId: 'UTC', reducedMotion: 'reduce', colorScheme: 'dark', deviceScaleFactor: 1 },
      }),
      // Large enough for the widest sheet (four large cells across five states).
      viewport: { width: 2400, height: 1800 },
      instances: [{ browser: 'chromium' }],
      screenshotFailures: false,
      expect: {
        toMatchScreenshot: {
          resolveScreenshotPath: ({ arg, ext }) => join(references, `${arg}${ext}`),
          resolveDiffPath: ({ arg, ext }) => join(visual, '.diffs', `${arg}${ext}`),
        },
      },
    },
  },
});
