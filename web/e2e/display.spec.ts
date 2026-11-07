import AxeBuilder from '@axe-core/playwright';
import { expect, test } from '@playwright/test';

// A wall display on the Demo Home (wave 33, docs/plan/15-v0.5.md): the owner
// makes one through the API, a fresh browser with no session opens its link,
// and sees the board with no chrome. The secret leaves the address at once,
// the display's cookie is scoped to /api/display, and neither the cookie nor
// the secret as a bearer token opens anything a session would. Revoking it
// signs the screen out.

const json = { Accept: 'application/json', 'Content-Type': 'application/json' };

test('a display shows its board with no chrome, and its credential opens nothing else', async ({ page, browser, baseURL }) => {
  // The owner's session (storageState): a board to show. A fresh hub builds its default board on the first visit.
  let boards = ((await (await page.request.get('/api/boards')).json()) as { boards: { id: string; name: string }[] }).boards;
  if (!boards.length) {
    await page.goto('/');
    await expect(page.locator('.tile').first()).toBeVisible();
    boards = ((await (await page.request.get('/api/boards')).json()) as { boards: { id: string; name: string }[] }).boards;
  }
  const board = boards[0]!;
  const made = await page.request.post('/api/displays', { headers: json, data: { name: 'e2e hall tablet', board: board.id, quiet: null } });
  expect(made.status(), await made.text()).toBe(201);
  const { display, secret } = (await made.json()) as { display: { id: string; name: string; board: { id: string } }; secret: string };
  expect(secret).toMatch(/^cdc_/);
  expect(display.board.id).toBe(board.id);

  // A screen with no session opens the link. The project's own storage
  // state is the owner's session, so this context starts empty.
  const signedOut = { storageState: { cookies: [], origins: [] } };
  const screen = await browser.newContext(signedOut);
  const tab = await screen.newPage();
  const errors: string[] = [];
  tab.on('pageerror', (e) => errors.push(e.message));
  await tab.goto(`/display/${board.id}#${secret}`);
  await expect(tab.locator('main.dsp')).toBeVisible();
  await expect(tab.locator('main.dsp .tile').first()).toBeVisible();
  // The secret is gone from the address bar.
  await expect(tab).toHaveURL(new RegExp(`/display/${board.id}$`));
  expect(new URL(tab.url()).hash).toBe('');
  expect(await tab.evaluate(() => JSON.stringify({ ...localStorage }) + JSON.stringify({ ...sessionStorage }))).not.toContain(secret);

  // No chrome: no navigation, no Inbox, no menus, no command bar, no sign-in.
  await expect(tab.getByRole('navigation')).toHaveCount(0);
  await expect(tab.getByRole('button', { name: /^Inbox/ })).toHaveCount(0);
  // A tile's menu ("Fleet menu") and the Settings menu; an incident's own
  // text may mention settings.
  await expect(tab.getByRole('button', { name: /(^|\s)menu$|^settings$/i })).toHaveCount(0);
  await expect(tab.locator('.brief')).toHaveCount(0);
  await tab.keyboard.press('ControlOrMeta+k');
  await expect(tab.getByRole('dialog')).toHaveCount(0);
  await expect(tab.getByText('This display isn’t signed in')).toHaveCount(0);

  // The demo changes tiles' states as it runs, and a state's colours fade in
  // over half a second: axe measures the colours they settle on.
  await tab.evaluate(() => document.documentElement.setAttribute('data-motion', 'none'));
  await tab.evaluate(() => Promise.all(document.getAnimations().filter((a) => a instanceof CSSTransition).map((a) => a.finished.catch(() => undefined))));
  const { violations } = await new AxeBuilder({ page: tab }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa']).analyze();
  // Each node with axe's own summary (for contrast: the colours and the ratio it measured).
  expect(violations.map((v) => `${v.id}: ${v.nodes.map((n) => `${n.target.join(' ')} (${n.failureSummary ?? ''})`).join(', ')}`)).toEqual([]);

  // The cookie is the display's, HttpOnly, sent only to /api/display.
  const cookie = (await screen.cookies()).find((c) => c.name === 'chadash_display');
  expect(cookie).toBeDefined();
  expect(cookie!.path).toBe('/api/display');
  expect(cookie!.httpOnly).toBe(true);
  expect(cookie!.sameSite).toBe('Strict');
  expect((await tab.request.get('/api/display')).status()).toBe(200);
  // The link was good once: the cookie holds a different secret, and a
  // second claim with the code is refused.
  expect(cookie!.value).not.toContain(secret);
  const again = await (await browser.newContext(signedOut)).request.post('/api/display/claim', { headers: json, data: { token: secret } });
  expect(again.status()).toBe(401);

  // Session routes refuse it: with the context's cookies, with the cookie sent by hand, and with the secret as a bearer.
  expect((await tab.request.get('/api/graph')).status()).toBe(401);
  expect((await tab.request.get('/api/boards')).status()).toBe(401);
  expect((await tab.request.get('/api/graph', { headers: { Cookie: `chadash_display=${cookie!.value}` } })).status()).toBe(401);
  const upgrade = { Connection: 'Upgrade', Upgrade: 'websocket', 'Sec-WebSocket-Version': '13', 'Sec-WebSocket-Key': 'dGhlIHNhbXBsZSBub25jZQ==' };
  expect((await tab.request.get('/ws', { headers: { ...upgrade, Cookie: `chadash_display=${cookie!.value}` } })).status()).toBe(401);
  // Neither the spent link nor the device's own secret works as a bearer.
  for (const credential of [secret, cookie!.value]) {
    for (const path of ['/api/graph', '/api/inbox', '/api/tokens']) {
      const res = await fetch(`${baseURL}${path}`, { headers: { Accept: 'application/json', Authorization: `Bearer ${credential}` } });
      expect(res.status, `${path} with ${credential.slice(0, 4)}…`).toBe(401);
    }
  }

  // Revoked: the display's own route refuses it, and the screen says it isn't signed in.
  expect((await page.request.delete(`/api/displays/${display.id}`)).status()).toBe(204);
  expect((await tab.request.get('/api/display')).status()).toBe(401);
  await tab.reload();
  await expect(tab.getByRole('heading', { name: 'This display isn’t signed in' })).toBeVisible();
  expect(errors).toEqual([]);
  await screen.close();
});

// A TV's board is drawn larger by the width its browser reports (plan 18,
// "TV zoom from the screen"): 1.5x on a 1080p TV, 2.5x on a 4K one.
test('a TV zooms by the screen it is on', async ({ page, browser }) => {
  let boards = ((await (await page.request.get('/api/boards')).json()) as { boards: { id: string }[] }).boards;
  if (!boards.length) {
    await page.goto('/');
    await expect(page.locator('.tile').first()).toBeVisible();
    boards = ((await (await page.request.get('/api/boards')).json()) as { boards: { id: string }[] }).boards;
  }
  const board = boards[0]!;
  const made = await page.request.post('/api/displays', { headers: json, data: { name: 'e2e living room TV', board: board.id, quiet: null, style: 'tv' } });
  expect(made.status(), await made.text()).toBe(201);
  const { display, secret } = (await made.json()) as { display: { id: string }; secret: string };

  const screen = await browser.newContext({ storageState: { cookies: [], origins: [] }, viewport: { width: 1920, height: 1080 } });
  const tab = await screen.newPage();
  await tab.goto(`/display/${board.id}#${secret}`);
  await expect(tab.locator('main.dsp[data-style="tv"] .tile').first()).toBeVisible();
  const zoom = () => tab.locator('.dsp-content').evaluate((el) => getComputedStyle(el).zoom);
  expect(await zoom()).toBe('1.5');
  await tab.setViewportSize({ width: 3840, height: 2160 });
  await expect.poll(zoom).toBe('2.5');

  expect((await page.request.delete(`/api/displays/${display.id}`)).status()).toBe(204);
  await screen.close();
});
