import { expect, test as setup } from '@playwright/test';

// The first run, as a new owner sees it: the owner account, the passkey offer (skipped: headless Chromium has no
// authenticator), then the front page. The other tests start signed in as
// this owner.
setup('first run: the owner account, then the front page', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByText('Make this hub yours.')).toBeVisible();
  await expect(page.getByLabel('Username')).toHaveValue('admin');
  await page.getByLabel('Username').fill('owner');
  await page.getByLabel('Password', { exact: true }).fill('correct horse battery staple');
  await page.getByLabel('Password again').fill('correct horse battery staple');
  await page.getByRole('button', { name: 'Create owner account' }).click();

  await expect(page.getByText('Add a passkey?')).toBeVisible();
  await page.getByRole('button', { name: 'Skip for now' }).click();

  await expect(page.getByRole('button', { name: /^Inbox/ })).toBeVisible();
  await page.context().storageState({ path: 'e2e/.auth/owner.json' });
});
