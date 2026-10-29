import { expect, test } from '@playwright/test';

// The safety ladder against the hub's real action engine, on the Demo
// Home's incident fixes. The demo runs in real time, so a fix's check
// afterwards takes simulated minutes: the tests follow a task as far as
// its check, through the page and through the API.

test('a T1 action runs at once, through plan, approve and its check', async ({ page }) => {
  await page.goto('/');
  // homeassistant's backup is the fourth problem: Needs attention shows three until Show all.
  await page.getByRole('button', { name: /^Show all \d+$/ }).click();
  const fix = page.getByRole('button', { name: /^Back up homeassistant now/ }).first();
  await expect(fix).toBeVisible();

  const approved = page.waitForResponse((r) => /\/api\/plans\/[^/]+\/approve$/.test(new URL(r.url()).pathname));
  await fix.click();
  const res = await approved;
  expect(res.status()).toBe(202);
  const { task } = (await res.json()) as { task: { id: string } };

  // The page says it started and that it's checking, as it goes.
  await expect(page.getByText(/Back up homeassistant now · checking it worked/).first()).toBeVisible();

  // The engine has the task, run as the owner who clicked.
  const got = await page.request.get(`/api/tasks/${task.id}`);
  expect(got.ok()).toBeTruthy();
  const t = (await got.json()) as { state: string; label: string; actor: { name: string } };
  expect(['running', 'verifying', 'succeeded']).toContain(t.state);
  expect(t.label).toBe('Back up homeassistant now');
  expect(t.actor.name).toBe('owner');
});

test('a T2 action asks for a hold, shows what it affects, and Cancel runs nothing', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('button', { name: /^Show all \d+$/ }).click();
  await page.getByRole('button', { name: /^Prune per policy/ }).first().click();

  const confirm = page.getByRole('alertdialog', { name: /Prune per policy/ });
  await expect(confirm).toBeVisible();
  // The blast radius from the Home Graph, before anything runs.
  await expect(confirm.getByText(/loses its backups/).first()).toBeVisible();
  await expect(confirm.getByRole('button', { name: 'Hold to prune' })).toBeVisible();

  await confirm.getByRole('button', { name: 'Cancel' }).click();
  await expect(confirm).toBeHidden();
  await expect(page.getByText(/Prune per policy ·/)).toHaveCount(0);
});
