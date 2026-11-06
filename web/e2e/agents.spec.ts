import { expect, test } from '@playwright/test';
import { addAuthenticator, addPasskey, signInWithPassword, stepUpWithPasskey } from './webauthn';

// An AI agent through MCP, with no agent and no person (H5 in
// docs/plan/17-test-automation.md; 14b's manual test 13): a scripted MCP
// client speaks the streamable HTTP transport to /mcp with an agent token,
// proposes a T2 action and withdraws a second proposal; the owner approves the first
// in the Inbox with a hold and a passkey (the virtual authenticator). The
// audit has both sides. The client is plain JSON-RPC, the protocol Claude
// Desktop and other clients speak; no SDK is linked.

const protocolVersion = '2025-06-18';
// The Demo Home's T2 actions; the token may propose any of them.
const t2Actions = ['pve.guest.restart', 'pbs.datastore.prune', 'container.update'];
const json = { Accept: 'application/json', 'Content-Type': 'application/json' };

type Structured = Record<string, unknown>;

class MCP {
  private id = 0;
  constructor(
    private readonly url: string,
    private readonly token: string,
  ) {}

  async send(method: string, params?: unknown, notification = false): Promise<{ status: number; body: unknown }> {
    const msg = notification ? { jsonrpc: '2.0', method, params } : { jsonrpc: '2.0', id: ++this.id, method, params };
    const res = await fetch(this.url, {
      method: 'POST',
      headers: { ...json, Accept: 'application/json, text/event-stream', Authorization: `Bearer ${this.token}`, 'MCP-Protocol-Version': protocolVersion },
      body: JSON.stringify(msg),
    });
    const text = await res.text();
    return { status: res.status, body: text ? JSON.parse(text) : undefined };
  }

  async call(name: string, args: unknown): Promise<{ isError: boolean; structured: Structured }> {
    const { status, body } = await this.send('tools/call', { name, arguments: args });
    expect(status).toBe(200);
    const result = (body as { result?: { isError?: boolean; structuredContent?: Structured } }).result;
    expect(result).toBeTruthy();
    return { isError: !!result?.isError, structured: result?.structuredContent ?? {} };
  }
}

test('an agent proposes over MCP; the owner approves with a hold and a passkey', async ({ page, context, baseURL }) => {
  const auth = await addAuthenticator(context, page);
  await signInWithPassword(page);
  await addPasskey(page);

  // Agents on, an agent token: the owner, stepped up with the passkey.
  expect((await stepUpWithPasskey(page)).status).toBe(200);
  const on = await page.request.put('/api/settings/agents', { headers: json, data: { enabled: true } });
  expect(on.ok(), await on.text()).toBe(true);
  const made = await page.request.post('/api/tokens', {
    headers: json,
    data: { name: 'e2e agent', kind: 'agent', scopes: ['graph.read', 'inbox.read', ...t2Actions.map((a) => `propose:${a}`)], expires_in_days: 1 },
  });
  expect(made.status(), await made.text()).toBe(201);
  const { secret } = (await made.json()) as { secret: string };

  // The token works on /mcp and nowhere else.
  const elsewhere = await fetch(`${baseURL}/api/graph`, { headers: { Accept: 'application/json', Authorization: `Bearer ${secret}` } });
  expect(elsewhere.status).toBe(403);
  expect(((await elsewhere.json()) as { error?: { code?: string } }).error?.code).toBe('agent_mcp_only');

  const mcp = new MCP(`${baseURL}/mcp`, secret);
  const init = await mcp.send('initialize', { protocolVersion, capabilities: {}, clientInfo: { name: 'chadash-e2e', version: '1' } });
  expect(init.status).toBe(200);
  expect((init.body as { result?: { protocolVersion?: string } }).result?.protocolVersion).toBe(protocolVersion);
  expect((await mcp.send('notifications/initialized', undefined, true)).status).toBe(202);
  const tools = (await mcp.send('tools/list')).body as { result: { tools: { name: string }[] } };
  expect(tools.result.tools.map((t) => t.name)).toEqual(expect.arrayContaining(['search', 'get_resource', 'propose', 'get_proposal', 'withdraw']));

  // A T2 action the agent may propose, on whatever the Demo Home offers it.
  const found = await mcp.call('search', { query: '', limit: 50 });
  const resources = (found.structured.resources as { id: string }[]) ?? [];
  expect(resources.length).toBeGreaterThan(1);
  let pick: { action: string; target: string } | undefined;
  for (const c of resources) {
    const r = await mcp.call('get_resource', { resource: c.id });
    const offers = (r.structured.actions as { action: string; available: boolean; tier: string }[]) ?? [];
    const o = offers.find((x) => t2Actions.includes(x.action) && x.available && x.tier === 'T2');
    if (o) {
      pick = { action: o.action, target: c.id };
      break;
    }
  }
  expect(pick, 'a T2 action the agent may propose').toBeTruthy();
  const { action, target } = pick as { action: string; target: string };

  // Propose one, withdraw a second: neither runs by itself.
  const proposed = await mcp.call('propose', { action, target, rationale: 'e2e: it stopped answering.' });
  expect(proposed.isError, JSON.stringify(proposed.structured)).toBe(false);
  const proposal = proposed.structured.proposal as { id: string; state: string };
  expect(proposal.state).toBe('awaiting_approval');
  const second = await mcp.call('propose', { action, target, rationale: 'e2e: to be withdrawn.' });
  const secondID = (second.structured.proposal as { id: string }).id;
  const withdrawn = await mcp.call('withdraw', { proposal: secondID, reason: 'e2e: not needed.' });
  expect(withdrawn.isError).toBe(false);
  expect(((await mcp.call('get_proposal', { proposal: secondID })).structured.proposal as { state: string }).state).toBe('expired');

  // The owner approves in the Inbox: the hold, then the passkey.
  await page.goto('/');
  await page.getByRole('button', { name: /^Inbox/ }).click();
  const item = page.getByRole('group').filter({ hasText: 'e2e: it stopped answering.' });
  await expect(item).toBeVisible();
  const hold = item.getByRole('button', { name: 'Hold to approve' });
  await hold.focus();
  await page.keyboard.down(' ');
  await page.waitForTimeout(1000);
  await page.keyboard.up(' ');
  const confirm = page.getByRole('dialog', { name: 'Confirm it’s you' });
  await expect(confirm).toBeVisible();
  await confirm.getByRole('button', { name: 'Use your passkey' }).click();
  await expect(page.getByText(/^Approved: /)).toBeVisible();

  // The agent sees it decided, and the audit names both sides.
  await expect
    .poll(async () => ((await mcp.call('get_proposal', { proposal: proposal.id })).structured.proposal as { state: string }).state, { timeout: 15_000 })
    .toBe('approved');
  const audit = await page.request.get('/api/audit?action=approval.approve&limit=5');
  expect(audit.ok()).toBe(true);
  const approvals = JSON.stringify(await audit.json());
  expect(approvals).toContain(proposal.id);
  expect(approvals).toContain('"step_up":"passkey"');
  const requested = JSON.stringify(await (await page.request.get('/api/audit?action=approval.request&limit=5')).json());
  expect(requested).toContain('e2e agent');

  // Agents off again, for whatever runs next on this hub.
  const { credentials } = await auth.cdp.send('WebAuthn.getCredentials', { authenticatorId: auth.id });
  expect(credentials.length).toBeGreaterThan(0);
  expect((await stepUpWithPasskey(page)).status).toBe(200);
  expect((await page.request.put('/api/settings/agents', { headers: json, data: { enabled: false } })).ok()).toBe(true);
  expect((await mcp.send('tools/list')).status).toBe(404);
});
