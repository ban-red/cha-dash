import { expect, type BrowserContext, type CDPSession, type Page } from '@playwright/test';

// Helpers for the passkey project (H4 in docs/plan/17-test-automation.md):
// Chromium's virtual authenticator, and the owner signed in on localhost
// with a passkey of their own.

export const ownerPassword = 'correct horse battery staple';

export interface Authenticator {
  cdp: CDPSession;
  id: string;
}

/** A platform authenticator with user verification, answering every ceremony at once. */
export async function addAuthenticator(context: BrowserContext, page: Page): Promise<Authenticator> {
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
  return { cdp, id: authenticatorId };
}

/** Signs the owner in with the password: the setup's session is 127.0.0.1's, not localhost's. */
export async function signInWithPassword(page: Page): Promise<void> {
  await page.goto('/');
  await page.getByLabel('Username').fill('owner');
  await page.getByLabel('Password', { exact: true }).fill(ownerPassword);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await expect(page.getByRole('button', { name: /^Inbox/ })).toBeVisible();
}

/** Adds a passkey from Settings → Security, the way a person does. */
export async function addPasskey(page: Page): Promise<void> {
  await page.goto('/settings/security');
  await page.getByRole('button', { name: 'Add a passkey' }).click();
  await expect(page.getByText(/^Passkey added/)).toBeVisible();
}

/** A step-up with the passkey: the hub's own ceremony, answered by the authenticator. */
export async function stepUpWithPasskey(page: Page): Promise<{ status: number; body: { until?: string } }> {
  return page.evaluate(async () => {
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
}
