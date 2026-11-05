import { expect, test } from '@playwright/test';
import { addAuthenticator, addPasskey, signInWithPassword, stepUpWithPasskey } from './webauthn';

// Passkeys with no person and no device (H4 in docs/plan/17-test-automation.md):
// Chromium's virtual authenticator, through the DevTools protocol, answers
// every WebAuthn ceremony as a platform authenticator with user
// verification would. Nothing in the hub is lowered: the passkey is
// registered, signs in and steps up through the same code a phone uses.
//
// The project runs on http://localhost (playwright.config.ts): a relying
// party ID can't be an IP address, so 127.0.0.1 offers no passkeys. It
// runs last, so the owner's new passkey doesn't change the other tests.

test('a passkey: added in Settings → Security, then it signs in and steps up', async ({ page, context }) => {
  const auth = await addAuthenticator(context, page);
  await signInWithPassword(page);
  await addPasskey(page);
  const { credentials } = await auth.cdp.send('WebAuthn.getCredentials', { authenticatorId: auth.id });
  expect(credentials).toHaveLength(1);
  const [passkey] = credentials;
  expect(passkey?.isResidentCredential).toBe(true);

  // Signed out, then back in with it alone: no username, no password. The
  // sign-in page asks for a passkey in the username field's autofill
  // (conditional mediation) as it loads, and the authenticator answers that
  // at once, as a phone's would once the person picks it.
  expect((await page.request.post('/api/auth/logout')).ok()).toBe(true);
  await page.goto('/');
  await expect(page.getByRole('button', { name: /^Inbox/ })).toBeVisible();
  const session = await (await page.request.get('/api/auth/session')).json();
  expect(session.session.method).toBe('passkey');
  expect(session.step_up_methods).toContain('passkey');

  const stepUp = await stepUpWithPasskey(page);
  expect(stepUp.status).toBe(200);
  expect(Date.parse(stepUp.body.until ?? '')).toBeGreaterThan(Date.now());
  const { credentials: after } = await auth.cdp.send('WebAuthn.getCredentials', { authenticatorId: auth.id });
  expect(after[0]?.signCount ?? 0).toBeGreaterThan(passkey?.signCount ?? 0);
});
