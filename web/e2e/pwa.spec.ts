import { expect, test } from '@playwright/test';

// The installable app (H6 in docs/plan/17-test-automation.md): the manifest
// a phone reads to install Cha Dash, the service worker that keeps the app
// shell, and the page with the network cut. A real handset adds nothing a
// test can't see here. Chromium: Playwright's WebKit doesn't run service
// workers; the WebKit projects check the iPhone and iPad layouts instead.

/** A PNG's width and height, from its IHDR chunk. */
function pngSize(b: Buffer): [number, number] {
  expect(b.subarray(1, 4).toString('latin1')).toBe('PNG');
  return [b.readUInt32BE(16), b.readUInt32BE(20)];
}

test('the manifest installs Cha Dash, with icons that are what they say', async ({ page, request }) => {
  await page.goto('/');
  const href = await page.locator('link[rel="manifest"]').getAttribute('href');
  expect(href).toBe('/manifest.webmanifest');
  const res = await request.get(href!);
  expect(res.ok()).toBe(true);
  const m = await res.json();
  expect(m).toMatchObject({ name: 'Cha Dash', start_url: '/', scope: '/', display: 'standalone' });
  // The colour a phone paints before the app runs: the HTML's, as served.
  // The app then sets the meta to the theme's background, in any CSS colour.
  const served = await (await request.get('/')).text();
  expect(served).toContain(`<meta name="theme-color" content="${m.theme_color}"`);

  const purposes = new Set<string>();
  for (const icon of m.icons as { src: string; sizes: string; type: string; purpose: string }[]) {
    const r = await request.get(icon.src);
    expect(r.ok(), icon.src).toBe(true);
    expect(r.headers()['content-type'], icon.src).toContain(icon.type);
    purposes.add(icon.purpose);
    if (icon.type === 'image/png') {
      const [w, h] = pngSize(await r.body());
      expect(`${w}x${h}`, icon.src).toBe(icon.sizes);
    }
  }
  // An Android home screen needs 192 and 512 and a maskable one.
  expect([...purposes]).toEqual(expect.arrayContaining(['any', 'maskable']));
  const sizes = (m.icons as { sizes: string }[]).map((i) => i.sizes);
  expect(sizes).toEqual(expect.arrayContaining(['192x192', '512x512']));
  // iOS reads apple-touch-icon instead.
  const touch = await page.locator('link[rel="apple-touch-icon"]').getAttribute('href');
  const t = await request.get(touch!);
  expect(t.ok()).toBe(true);
  expect(pngSize(await t.body())[0]).toBeGreaterThanOrEqual(180);
});

test('the service worker keeps the app shell, and never the API', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByRole('button', { name: /^Inbox/ })).toBeVisible();
  const script = await page.evaluate(async () => (await navigator.serviceWorker.ready).active?.scriptURL);
  expect(new URL(script!).pathname).toBe('/sw.js');
  const cached = await page.evaluate(async () => {
    const out: string[] = [];
    for (const name of await caches.keys()) {
      for (const req of await (await caches.open(name)).keys()) out.push(`${name} ${new URL(req.url).pathname}`);
    }
    return out;
  });
  expect(cached.some((c) => /^chadash-shell-\S+ \/index\.html$/.test(c))).toBe(true);
  expect(cached.some((c) => c.endsWith(' /boot.js'))).toBe(true);
  expect(cached.some((c) => c.endsWith(' /manifest.webmanifest'))).toBe(true);
  expect(cached.filter((c) => / \/(api|ws)(\/|$)/.test(c))).toEqual([]);
});

test('with the network cut, the page opens on its last-known state', async ({ page, context }) => {
  await page.goto('/');
  await expect(page.getByText('Nextcloud is stuck in maintenance mode').first()).toBeVisible();
  await page.evaluate(async () => {
    await navigator.serviceWorker.ready;
  });
  // A controlled page: the next navigation goes through the worker.
  await page.reload();
  await expect(page.getByText('Nextcloud is stuck in maintenance mode').first()).toBeVisible();
  expect(await page.evaluate(() => !!navigator.serviceWorker.controller)).toBe(true);

  await context.setOffline(true);
  try {
    await page.reload();
    // The shell came from the cache, and the page says it's the last-known one.
    await expect(page.getByRole('status').filter({ hasText: 'Cha Dash hub unreachable' })).toBeVisible({ timeout: 20_000 });
    await expect(page.getByText('Nextcloud is stuck in maintenance mode').first()).toBeVisible();
  } finally {
    await context.setOffline(false);
  }
  // Back online, it reconnects on its own.
  await expect(page.getByRole('status').filter({ hasText: 'Cha Dash hub unreachable' })).toBeHidden({ timeout: 30_000 });
});
