import { defineConfig, devices } from '@playwright/test';

// End-to-end tests against a real hub: the release binary with the SPA
// embedded, on the Demo Home (e2e/global-setup.ts starts it). How to run
// them: CONTRIBUTING.md, "End-to-end tests".
const port = Number(process.env.CHADASH_E2E_PORT ?? 3698);
// A browser of your own, for systems Playwright can't install one on.
const executablePath = process.env.CHADASH_E2E_CHROMIUM;
const owner = 'e2e/.auth/owner.json';

export default defineConfig({
  testDir: 'e2e',
  globalSetup: './e2e/global-setup.ts',
  // One hub, one owner and one demo clock are shared, so the tests run in
  // order. A failure is a bug to find, so nothing is retried.
  workers: 1,
  fullyParallel: false,
  retries: 0,
  forbidOnly: !!process.env.CI,
  reporter: process.env.CI ? [['github'], ['html', { open: 'never' }]] : 'list',
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    trace: 'retain-on-failure',
    launchOptions: executablePath ? { executablePath } : {},
  },
  projects: [
    { name: 'first-run', testMatch: /first-run\.setup\.ts/ },
    {
      name: 'desktop',
      use: { ...devices['Desktop Chrome'], storageState: owner },
      dependencies: ['first-run'],
    },
    {
      name: 'phone',
      use: { ...devices['Pixel 7'], storageState: owner },
      dependencies: ['first-run'],
      testMatch: /front-page\.spec\.ts/,
    },
  ],
});
