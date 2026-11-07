import { expect, test } from '@playwright/test';

// Printing a board (plan 18, "Print with absolute times"): the Settings
// menu's Print opens /print/<board>, which renders the board at the ink
// level, where times are absolute, calls the browser's print, and goes back
// to the board afterwards.

test('Print renders the board with absolute times and calls print', async ({ page }) => {
  await page.addInitScript(() => {
    const w = window as unknown as { printed: number };
    w.printed = 0;
    window.print = () => {
      w.printed++;
      window.dispatchEvent(new Event('afterprint'));
    };
  });
  await page.goto('/');
  await expect(page.locator('.tile').first()).toBeVisible();
  await page.getByRole('button', { name: 'Settings' }).click();
  await page.getByRole('menuitem', { name: 'Print this board' }).click();

  // It prints once, then goes back to the board it printed.
  await expect.poll(() => page.evaluate(() => (window as unknown as { printed: number }).printed)).toBe(1);
  await expect(page).toHaveURL(/\/boards\/[^/]+$/);

  // The print page itself, with print stubbed to stay: ink tiles only, no relative times, no chrome.
  const board = new URL(page.url()).pathname.split('/').pop()!;
  await page.addInitScript(() => {
    window.print = () => undefined;
  });
  await page.goto(`/print/${board}`);
  await expect(page.locator('main.print-page .tile.lvl-ink').first()).toBeVisible();
  await expect(page.locator('main.print-page .tile:not(.lvl-ink)')).toHaveCount(0);
  await expect(page.locator('main.page')).toHaveCount(0);
  expect(await page.locator('main.print-page').innerText()).not.toMatch(/\bago\b|just now/);
});
