import { expect, test } from '@playwright/test';

// Passkeys with no person and no device (H4 in docs/plan/17-test-automation.md):
// Chromium's virtual authenticator, through the DevTools protocol, answers
// every WebAuthn ceremony as a platform authenticator with user
// verification would. Nothing in the hub is lowered: the passkey is
// registered, signs in and steps up through the same code a phone uses.
//
// The project runs on http://localhost (playwright.config.ts): a relying
// party ID can't be an IP address, so 127.0.0.1 offers no passkeys. It
// runs last, so the owner's new passkey doesn't change the other tests.

const password = 'correct horse battery staple';

test('a passkey: added in Settings → Security, then it signs in and steps up', async ({ page, context }) => {
  const cdp = await context.newCDPSession(page);
  await cdp.send('WebAuthn.enable');
  const { authenticatorId } = await cdp.send('WebAuthn.addVirtualAuthenticator', {
    options: {
      protocol: 'ctap2',
      transport: 'internal',
      hasResidentKey: true,
      hasUserVerification: true,
      isUserVerified: true,
      automaticPresenceSimulation: true,
    },
  });

  // Signed in with the password first: the setup's session is 127.0.0.1's.
  await page.goto('/');
  await page.getByLabel('Username').fill('owner');
  await page.getByLabel('Password', { exact: true }).fill(password);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await expect(page.getByRole('button', { name: /^Inbox/ })).toBeVisible();

  // Add one, the way a person does.
  await page.goto('/settings/security');
  await page.getByRole('button', { name: 'Add a passkey' }).click();
  await expect(page.getByText(/^Passkey added/)).toBeVisible();
  const { credentials } = await cdp.send('WebAuthn.getCredentials', { authenticatorId });
  expect(credentials).toHaveLength(1);
  const [passkey] = credentials;
  expect(passkey?.isResidentCredential).toBe(true);

  // Signed out, then back in with it alone: no username, no password.
  expect((await page.request.post('/api/auth/logout')).ok()).toBe(true);
  await page.goto('/');
  await page.getByRole('button', { name: 'Sign in with a passkey' }).click();
  await expect(page.getByRole('button', { name: /^Inbox/ })).toBeVisible();
  const session = await (await page.request.get('/api/auth/session')).json();
  expect(session.session.method).toBe('passkey');
  expect(session.step_up_methods).toContain('passkey');

  // A step-up with it: the hub's ceremony, answered by the authenticator.
  const stepUp = await page.evaluate(async () => {
    type JSONOptions = { parseRequestOptionsFromJSON(o: unknown): PublicKeyCredentialRequestOptions };
    const begin = await fetch('/api/auth/step-up/begin', { method: 'POST', headers: { Accept: 'application/json' } }).then((r) => r.json());
    const publicKey = (PublicKeyCredential as unknown as JSONOptions).parseRequestOptionsFromJSON(begin.publicKey);
    const cred = (await navigator.credentials.get({ publicKey })) as PublicKeyCredential;
    const res = await fetch('/api/auth/step-up', {
      method: 'POST',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
      body: JSON.stringify(cred.toJSON()),
    });
    return { status: res.status, body: await res.json() };
  });
  expect(stepUp.status).toBe(200);
  expect(Date.parse(stepUp.body.until)).toBeGreaterThan(Date.now());
  const { credentials: after } = await cdp.send('WebAuthn.getCredentials', { authenticatorId });
  expect(after[0]?.signCount ?? 0).toBeGreaterThan(passkey?.signCount ?? 0);
});
