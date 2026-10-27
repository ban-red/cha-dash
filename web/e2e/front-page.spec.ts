import AxeBuilder from '@axe-core/playwright';
import { expect, test } from '@playwright/test';

// The front page on the Demo Home, desktop and phone. The demo runs on the
// wall clock from 14:05, so incidents come and go while the suite runs:
// these tests look for kinds of things, never exact counts.

test('the Demo Home fills the front page', async ({ page }) => {
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(e.message));

  await page.goto('/');
  // The status sentence leads, then Needs attention with the day's incidents.
  await expect(page.getByRole('heading', { level: 1 })).toContainText(/need(s)? you|All good|healthy/);
  await expect(page.getByText(/^Needs attention/i)).toBeVisible();
  await expect(page.getByText('Backup of homeassistant failed').first()).toBeVisible();
  // Tiles from the board, live from the simulator.
  for (const tile of ['Fleet', 'Cameras', 'Containers']) {
    await expect(page.getByText(tile, { exact: true }).first()).toBeVisible();
  }
  expect(errors).toEqual([]);
});

// WCAG 2.2 AA is a release gate (01 · Vision, the Pact). The unit tests run
// axe over every view in happy-dom; this runs it on the real page, laid out
// and styled, in every built-in theme.
for (const theme of ['instrument-dark', 'instrument-light', 'oled', 'night', 'high-contrast', 'paper']) {
  test(`no WCAG 2.2 AA violations in ${theme}`, async ({ page }) => {
    await page.addInitScript((id) => window.localStorage.setItem('chadash.theme', id), theme);
    await page.goto('/');
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
    await expect(page.getByText('Backup of homeassistant failed').first()).toBeVisible();
    const { violations } = await new AxeBuilder({ page })
      .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'])
      .analyze();
    expect(violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`)).toEqual([]);
  });
}

test('the command bar works from the keyboard', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByRole('button', { name: /^Inbox/ })).toBeVisible();
  await page.keyboard.press('ControlOrMeta+k');
  const bar = page.getByRole('dialog', { name: 'Command bar' });
  await expect(bar).toBeVisible();
  await page.keyboard.type('homeassistant');
  await expect(bar.getByText(/homeassistant/i).first()).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(bar).toBeHidden();
});
