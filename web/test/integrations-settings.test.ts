import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, mount } from '@vue/test-utils';
import { nextTick } from 'vue';
import IntegrationEditor from '../src/settings/IntegrationEditor.vue';
import IntegrationsPane from '../src/settings/IntegrationsPane.vue';
import TestResultView from '../src/settings/TestResultView.vue';
import { route, syncRoute } from '../src/lib/router';
import { choices, clean, fieldLabel, initialValue, kindOf, secretPaths, validate, type JsonSchema } from '../src/lib/schema';
import { groupCatalog, isWrite, normalizeGrant } from '../src/model/integrations';
import { resetIntegrations } from '../src/stores/integrations';
import { fakeFetch } from './fake-fetch';
import { harness } from './harness';

const PLEX_SCHEMA: JsonSchema = {
  type: 'object',
  required: ['url', 'token'],
  properties: {
    url: { type: 'string', format: 'uri', title: 'URL', examples: ['https://plex.home.arpa:32400'] },
    token: { type: 'string', writeOnly: true, title: 'Token', minLength: 8 },
    tls: {
      type: 'object',
      title: 'TLS',
      properties: {
        mode: { enum: ['verify', 'pin', 'tofu', 'insecure'], default: 'verify' },
        pin: { type: 'string', pattern: '^[0-9a-f]{64}$', examples: ['ab12…'] },
      },
      if: { properties: { mode: { const: 'pin' } }, required: ['mode'] },
      then: { required: ['pin'] },
    },
    poll_s: { type: 'integer', minimum: 10, maximum: 3600, title: 'Poll every (s)' },
  },
};

const CHECKS_SCHEMA: JsonSchema = {
  type: 'object',
  properties: {
    targets: {
      type: 'array',
      title: 'Targets',
      minItems: 1,
      items: {
        type: 'object',
        required: ['name', 'kind'],
        properties: {
          name: { type: 'string' },
          kind: { enum: ['http', 'tcp'], default: 'http' },
          url: { type: 'string', format: 'uri', 'x-chadash-when': { kind: 'http' } },
          host: { type: 'string', 'x-chadash-when': { kind: 'tcp' } },
        },
      },
      'x-chadash-item-test': true,
    },
  },
};

const LEAK = 'plex-token-from-hub-do-not-show';

function hub(over: Record<string, unknown> = {}) {
  return fakeFetch({
    'GET /api/integrations/catalog': {
      status: 200,
      body: [
        { id: 'plex', name: 'Plex', version: '1.0.0', tier: 0, category: 'media', capabilities: ['media'], config_schema: PLEX_SCHEMA },
        { id: 'checks', name: 'Checks', version: '1.0.0', tier: 0, category: 'monitoring', config_schema: CHECKS_SCHEMA },
      ],
    },
    'GET /api/integrations/instances': {
      status: 200,
      body: [
        { id: 'i1', integration: 'plex', name: 'Plex', status: 'repair', version: '1.41.2', last_error: 'Plex rejected its token since 13:27.', warnings: [] },
        { id: 'i2', integration: 'checks', name: 'Checks', status: 'ok', warnings: [{ code: 'http_creds', severity: 'attention', message: 'Sends credentials over http://' }] },
      ],
    },
    'GET /api/integrations/instances/i1': {
      status: 200,
      body: {
        id: 'i1',
        integration: 'plex',
        name: 'Plex',
        status: 'repair',
        last_error: 'Plex rejected its token since 13:27.',
        // A hub that wrongly sends a secret back must still never see it shown.
        config: { url: 'https://plex.lan:32400', token: LEAK, tls: { mode: 'verify' } },
        secrets: { token: { set: true } },
      },
    },
    'PUT /api/integrations/instances/i1': { status: 200, body: {} },
    'POST /api/integrations/instances': { status: 201, body: { id: 'i9' } },
    'DELETE /api/integrations/instances/i1': { status: 204 },
    'POST /api/integrations/instances/i1/test': {
      status: 200,
      body: { ok: true, version: '1.41.2', latency_ms: 84.4, grants: [], warnings: [] },
    },
    'POST /api/integrations/test': {
      status: 200,
      body: {
        ok: true,
        version: '1.41.2',
        latency_ms: 91,
        grants: [{ name: 'library.read', access: 'read' }, { name: 'server.settings', access: 'write', detail: 'change server settings' }, 'VM.PowerMgmt'],
        warnings: [{ code: 'write_grant_on_read_only', severity: 'attention', message: 'This token can change settings; Cha Dash only reads.' }],
      },
    },
    ...over,
  });
}

beforeEach(() => {
  history.replaceState(null, '', '/settings/integrations');
  syncRoute();
  resetIntegrations();
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

async function editor(props: Record<string, unknown>) {
  const h = harness();
  const w = h.mountIn(IntegrationEditor, props);
  await flushPromises();
  await nextTick();
  return { ...h, w };
}

const input = (id: string) => document.getElementById(id) as HTMLInputElement;
async function typeInto(id: string, value: string) {
  const el = input(id);
  el.value = value;
  el.dispatchEvent(new Event('input'));
  el.dispatchEvent(new Event('blur'));
  await nextTick();
}
const button = (text: string) => [...document.body.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim().startsWith(text))!;

describe('the schema subset', () => {
  it('labels, kinds and starting values', () => {
    expect(fieldLabel('api_key', {})).toBe('API key');
    expect(fieldLabel('token_id', {})).toBe('Token ID');
    expect(kindOf({ type: 'string', writeOnly: true })).toBe('secret');
    expect(kindOf({ enum: ['a', 'b'] })).toBe('enum');
    expect(kindOf({ type: ['integer', 'null'] })).toBe('integer');
    // The hub's labelled choices carry both enum and oneOf (Unraid's mode): the titles win.
    const mode: JsonSchema = {
      type: 'string',
      enum: ['read_only', 'control'],
      oneOf: [
        { const: 'read_only', title: 'Read-only' },
        { const: 'control', title: 'Read-only, and let Cha Dash control Unraid' },
      ],
      default: 'read_only',
    };
    expect(kindOf(mode)).toBe('enum');
    expect(choices(mode)).toEqual([
      { value: 'read_only', label: 'Read-only' },
      { value: 'control', label: 'Read-only, and let Cha Dash control Unraid' },
    ]);
    expect(initialValue({ type: 'object', properties: { mode } }, {})).toEqual({ mode: 'read_only' });
    // secrets never come back as values, at any depth
    expect(initialValue(PLEX_SCHEMA, { url: 'https://x', token: LEAK })).toEqual({ url: 'https://x', tls: { mode: 'verify' } });
    expect(initialValue({ type: 'object', properties: { a: { type: 'object', properties: { s: { type: 'string', writeOnly: true } } } } }, { a: { s: LEAK } })).toEqual({ a: {} });
    expect(secretPaths(PLEX_SCHEMA)).toEqual(['token']);
  });

  it('validates in plain words, with if/then and stored secrets', () => {
    const none = { secretsSet: new Set<string>(), secrets: {} };
    expect(validate(PLEX_SCHEMA, { tls: { mode: 'verify' } }, none)).toEqual({ url: 'Required.', token: 'Required.' });
    expect(validate(PLEX_SCHEMA, { url: 'plex.lan', tls: { mode: 'verify' } }, { ...none, secretsSet: new Set(['token']) })).toEqual({
      url: 'Enter a full URL, like https://plex.home.arpa:32400',
    });
    expect(validate(PLEX_SCHEMA, { url: 'https://p', tls: { mode: 'pin' }, poll_s: 5 }, { secretsSet: new Set(), secrets: { token: 'short' } })).toEqual({
      token: 'At least 8 characters.',
      'tls.pin': 'Required.',
      poll_s: 'Between 10 and 3600.',
    });
    expect(validate(PLEX_SCHEMA, { url: 'https://p', tls: { mode: 'pin', pin: 'nope' }, poll_s: 1.5 }, { secretsSet: new Set(['token']), secrets: {} })).toEqual({
      'tls.pin': 'That doesn’t look right. Example: ab12…',
      poll_s: 'Enter a whole number.',
    });
    expect(validate(CHECKS_SCHEMA, { targets: [{ name: 'nas', kind: 'icmp' }] }, none)).toEqual({ 'targets.0.kind': 'Pick one of the options.' });
  });

  it('cleans for sending: blanks and inactive branches out, secrets never in config', () => {
    expect(clean(PLEX_SCHEMA, { url: ' https://p ', token: LEAK, tls: { mode: 'verify', pin: '' }, poll_s: undefined })).toEqual({
      url: 'https://p',
      tls: { mode: 'verify' },
    });
    const branchy: JsonSchema = {
      type: 'object',
      properties: { mode: { enum: ['verify', 'pin'] } },
      if: { properties: { mode: { const: 'pin' } }, required: ['mode'] },
      then: { properties: { pin: { type: 'string' } }, required: ['pin'] },
    };
    expect(clean(branchy, { mode: 'verify', pin: 'stale' })).toEqual({ mode: 'verify' });
    expect(clean(branchy, { mode: 'pin', pin: 'abc' })).toEqual({ mode: 'pin', pin: 'abc' });
  });
});

describe('groupCatalog', () => {
  it('orders groups as the Add list does, and puts the uncategorised last', () => {
    const g = groupCatalog([{ id: 'a', category: 'files' }, { id: 'b' }, { id: 'c', category: 'media' }, { id: 'd', category: 'unheard-of' }]);
    expect(g.map((x) => [x.label, x.items.map((i) => i.id)])).toEqual([
      ['Media', ['c']],
      ['Files & notes', ['a']],
      ['Other', ['b', 'd']],
    ]);
  });
});

describe('Settings → Integrations', () => {
  it('lists instances with status and warnings', async () => {
    hub();
    const h = harness();
    h.mountIn(IntegrationsPane);
    await flushPromises();
    const rows = [...document.body.querySelectorAll('.st-row')].map((r) => r.textContent?.replace(/\s+/g, ' ').trim());
    // Trouble first: the repair sits above the healthy instance.
    expect(rows[0]).toContain('Plex rejected its token since 13:27.');
    expect(rows[0]).toContain('Needs repair');
    expect(rows[1]).toContain('Checks');
    expect(rows[1]).toContain('1 warning');
    expect(rows[1]).toContain('Reading');
  });

  it('shows the catalog to add from', async () => {
    hub();
    history.replaceState(null, '', '/settings/integrations/new');
    syncRoute();
    const h = harness();
    h.mountIn(IntegrationsPane);
    await flushPromises();
    // Grouped by category, in the Add list's order: Media before Monitoring.
    expect([...document.body.querySelectorAll('.cat-h')].map((t) => t.firstChild?.textContent)).toEqual(['Media', 'Monitoring']);
    expect([...document.body.querySelectorAll('.cat-card .t')].map((t) => t.textContent)).toEqual(['Plex', 'Checks']);
    (document.body.querySelectorAll('.cat-card')[1] as HTMLAnchorElement).click();
    expect(route.value.path).toBe('/settings/integrations/new/checks');
  });

  it('filters the catalog to a flat list without headings', async () => {
    hub();
    history.replaceState(null, '', '/settings/integrations/new');
    syncRoute();
    const h = harness();
    const w = h.mountIn(IntegrationsPane);
    await flushPromises();
    await w.find('#catalog-q').setValue('plex');
    expect(document.body.querySelector('.cat-h')).toBeNull();
    expect([...document.body.querySelectorAll('.cat-card .t')].map((t) => t.textContent)).toEqual(['Plex']);
  });

  it('never prefills or shows a secret, says it is set, and focuses it for a repair', async () => {
    hub();
    await editor({ instanceRef: 'i1', reason: 'auth' });
    const token = input('f-token');
    expect(token.type).toBe('password');
    expect(token.value).toBe('');
    expect(token.placeholder).toBe('Stored in the vault · type to replace');
    expect(token.closest('.field')!.querySelector('.set-chip')!.textContent).toContain('set');
    expect(document.body.innerHTML).not.toContain(LEAK);
    expect(document.activeElement).toBe(token);
    expect(token.closest('.field')!.classList.contains('st-focus')).toBe(true);
    expect(document.body.querySelector('.st-note.bad')!.textContent).toContain('Paste a new token below');
    expect(input('f-url').value).toBe('https://plex.lan:32400');
  });

  it('resolves a repair’s name to the instance’s address', async () => {
    hub();
    await editor({ instanceRef: 'Plex', reason: 'auth' });
    expect(route.value.path).toBe('/settings/integrations/i1');
    expect(route.value.query).toEqual({ reason: 'auth' });
  });

  it('saves only what changed; secrets only when typed, then forgets them', async () => {
    const f = hub();
    await editor({ instanceRef: 'i1' });
    await typeInto('f-url', 'https://plex.home.arpa:32400');
    button('Save changes').click();
    await flushPromises();
    expect(f.last('PUT', '/api/integrations/instances/i1')!.body).toEqual({ config: { url: 'https://plex.home.arpa:32400', tls: { mode: 'verify' } } });

    await typeInto('f-token', 'fresh-token-123');
    button('Save changes').click();
    await flushPromises();
    // the hub still has the old URL (this fake doesn't store), so only the secret changed
    expect(f.last('PUT', '/api/integrations/instances/i1')!.body).toEqual({ secrets: { token: 'fresh-token-123' } });
    expect(input('f-token').value).toBe('');
    expect(f.calls.filter((c) => c.method !== 'GET').every((c) => !JSON.stringify(c.body ?? '').includes(LEAK))).toBe(true);
  });

  it('checks the form before saving', async () => {
    const f = hub();
    await editor({ instanceRef: 'i1' });
    await typeInto('f-url', '');
    button('Save changes').click();
    await flushPromises();
    expect(document.getElementById('f-url-err')!.textContent).toBe('Required.');
    expect(input('f-url').getAttribute('aria-invalid')).toBe('true');
    expect(document.activeElement).toBe(input('f-url'));
    expect(f.calls.some((c) => c.method === 'PUT')).toBe(false);
  });

  it('adds a new instance with its secrets, then opens it', async () => {
    const f = hub();
    await editor({ integrationId: 'plex' });
    expect(document.activeElement).toBe(input('f-name'));
    expect(input('f-name').value).toBe('Plex');
    button('Save').click();
    await flushPromises();
    expect(document.getElementById('f-url-err')!.textContent).toBe('Required.');
    expect(document.getElementById('f-token-err')!.textContent).toBe('Required.');
    await typeInto('f-url', 'https://plex.lan:32400');
    await typeInto('f-token', 'abcdefgh1234');
    // the TLS mode is a choice of four
    expect([...document.body.querySelectorAll('.choice-row label')].map((l) => l.textContent)).toEqual(['verify', 'pin', 'tofu', 'insecure']);
    button('Save').click();
    await flushPromises();
    expect(f.last('POST', '/api/integrations/instances')!.body).toEqual({
      integration: 'plex',
      name: 'Plex',
      config: { url: 'https://plex.lan:32400', tls: { mode: 'verify' } },
      secrets: { token: 'abcdefgh1234' },
    });
    expect(route.value.path).toBe('/settings/integrations/i9');
  });

  it('edits lists of objects (Checks targets)', async () => {
    hub();
    await editor({ integrationId: 'checks' });
    button('Add target').click();
    await nextTick();
    expect(document.body.querySelector('.st-array-item legend')!.textContent).toBe('Target 1');
    button('Save').click();
    await flushPromises();
    expect(document.getElementById('f-targets-0-name-err')!.textContent).toBe('Required.');
    button('Remove target 1').click();
    await nextTick();
    expect(document.body.querySelector('.st-array-item')).toBeNull();
  });

  it('folds an optional group, says what it holds, and opens it on an error inside', async () => {
    hub();
    await editor({ instanceRef: 'i1' });
    const fold = document.body.querySelector<HTMLDetailsElement>('details.st-fold')!;
    expect(fold.open).toBe(false);
    expect(fold.querySelector('.st-fold-title')!.textContent).toBe('TLS');
    expect(fold.querySelector('.st-fold-sum')!.textContent).toBe('verify');

    const pin = [...fold.querySelectorAll<HTMLInputElement>('input[type="radio"]')].find((r) => r.value === 'pin')!;
    pin.checked = true;
    pin.dispatchEvent(new Event('change'));
    await nextTick();
    button('Save').click();
    await flushPromises();
    // pin mode needs a fingerprint: the error opens the group
    expect(fold.open).toBe(true);
    await typeInto('f-tls-pin', 'a'.repeat(64));
    expect(fold.querySelector('.st-fold-sum')!.textContent).toBe('pin · aaaaaaaa…');
  });

  it('shows a target’s fields for its kind, and tests one target alone', async () => {
    const f = hub({
      'GET /api/integrations/instances/i2': {
        status: 200,
        body: { id: 'i2', integration: 'checks', name: 'Checks', status: 'ok', config: { targets: [{ name: 'Web', kind: 'http', url: 'https://web.lab.example' }, { name: 'SSH', kind: 'tcp', host: 'pluto.lab.example' }] }, secrets: {} },
      },
      'POST /api/integrations/test': { status: 200, body: { ok: true, latency_ms: 12.3, warnings: [{ code: 'tls_untrusted', severity: 'warning', message: 'answering, but the certificate isn’t trusted.' }] } },
    });
    await editor({ instanceRef: 'i2' });
    expect(document.getElementById('f-targets-0-url')).not.toBeNull();
    expect(document.getElementById('f-targets-0-host')).toBeNull();
    expect(document.getElementById('f-targets-1-url')).toBeNull();
    expect(document.getElementById('f-targets-1-host')).not.toBeNull();

    button('Test target 2').click();
    await flushPromises();
    expect(f.last('POST', '/api/integrations/test')!.body).toEqual({
      instance_id: 'i2',
      config: { targets: [{ name: 'SSH', kind: 'tcp', host: 'pluto.lab.example' }] },
      secrets: {},
    });
    const note = document.body.querySelectorAll('.st-array-item')[1]!.querySelector('.item-test')!;
    expect(note.textContent).toContain('Answered in 12 ms. Answering, but the certificate isn’t trusted.');
    expect(note.classList.contains('attn')).toBe(true);

    // Switching a target to tcp drops its URL from what's saved.
    const tcp = [...document.body.querySelectorAll<HTMLInputElement>('input[name="f-targets-0-kind"]')].find((r) => r.value === 'tcp')!;
    tcp.checked = true;
    tcp.dispatchEvent(new Event('change'));
    await nextTick();
    expect(document.getElementById('f-targets-0-url')).toBeNull();
    button('Test target 1').click();
    await flushPromises();
    expect(f.last('POST', '/api/integrations/test')!.body).toMatchObject({ config: { targets: [{ name: 'Web', kind: 'tcp' }] } });
  });

  it('tests a saved instance as stored, and unsaved changes with what was typed', async () => {
    const f = hub();
    await editor({ instanceRef: 'i1' });
    button('Test connection').click();
    await flushPromises();
    expect(f.last('POST', '/api/integrations/instances/i1/test')).toBeDefined();
    expect(document.body.querySelector('.test-result')!.textContent).toContain('Connected. Version 1.41.2. Answered in 84 ms.');

    await typeInto('f-url', 'https://plex.home.arpa:32400');
    button('Test connection').click();
    await flushPromises();
    // unsaved changes go with the instance id; the hub fills in the stored token
    expect(f.last('POST', '/api/integrations/test')!.body).toEqual({
      instance_id: 'i1',
      config: { url: 'https://plex.home.arpa:32400', tls: { mode: 'verify' } },
      secrets: {},
    });

    await typeInto('f-token', 'retyped-token-1');
    button('Test connection').click();
    await flushPromises();
    expect(f.last('POST', '/api/integrations/test')!.body).toEqual({
      instance_id: 'i1',
      config: { url: 'https://plex.home.arpa:32400', tls: { mode: 'verify' } },
      secrets: { token: 'retyped-token-1' },
    });
  });

  it('deletes after a confirmation in place', async () => {
    const f = hub();
    const { ui } = await editor({ instanceRef: 'i1' });
    button('Delete…').click();
    await nextTick();
    const confirm = document.body.querySelector('.st-confirm')!;
    expect(confirm.getAttribute('role')).toBe('alertdialog');
    expect(confirm.textContent).toContain('its stored secrets are erased from the vault');
    expect(f.calls.some((c) => c.method === 'DELETE')).toBe(false);
    (confirm.querySelector('.btn.danger') as HTMLButtonElement).click();
    await flushPromises();
    expect(f.last('DELETE', '/api/integrations/instances/i1')).toBeDefined();
    expect(route.value.path).toBe('/settings/integrations');
    expect(ui.state.toasts[0]!.text).toContain('Deleted Plex');
  });

  it('offers to add the integration when a repair names one that isn’t connected', async () => {
    hub({ 'GET /api/integrations/instances': { status: 200, body: [] } });
    await editor({ instanceRef: 'plex', reason: 'auth' });
    expect(document.body.querySelector('.st-empty .t')!.textContent).toBe('Plex isn’t connected on this hub');
    (document.body.querySelector('.st-empty .btn.primary') as HTMLAnchorElement).click();
    expect(route.value.path).toBe('/settings/integrations/new/plex');
    expect(route.value.query).toEqual({ reason: 'auth' });
  });
});

describe('Test connection results', () => {
  it('shows grants and flags write grants on a read-only connection', () => {
    const w = mount(TestResultView, {
      props: {
        result: {
          ok: true,
          version: '7.1',
          latency_ms: 12,
          grants: [{ name: 'Sys.Audit', access: 'read' }, { name: 'VM.PowerMgmt' }, 'containers:write'],
          warnings: [{ code: 'write_grant_on_read_only', severity: 'attention', message: 'This token can power guests off.' }],
        },
      },
    });
    const rows = w.findAll('.grants li');
    expect(rows.map((r) => r.classes('flagged'))).toEqual([false, true, true]);
    expect(rows[1]!.text()).toContain('write · not needed');
    expect(w.find('.st-note').classes()).toContain('attn');
    expect(w.find('.st-note').text()).toContain('2 write grants');
    expect(w.text()).toContain('This token can power guests off.');
  });

  it('doesn’t flag writes when the connection may write', () => {
    const w = mount(TestResultView, { props: { readOnly: false, result: { ok: true, grants: ['containers:write'] } } });
    expect(w.find('.grants li').classes('flagged')).toBe(false);
    expect(w.find('.grants li').text()).toContain('write');
  });

  it('says why it failed, in words', () => {
    const w = mount(TestResultView, { props: { result: { ok: false, error: { code: 'unauthorized', message: 'Plex answered 401: the token was rejected.' } } } });
    expect(w.find('.st-note').classes()).toContain('bad');
    expect(w.text()).toContain('Couldn’t connect. Plex answered 401: the token was rejected.');
    expect(w.text()).toContain('unauthorized');
  });

  it('normalises grants from strings or objects', () => {
    expect(normalizeGrant('read:vms')).toEqual({ name: 'read:vms', access: 'read' });
    expect(isWrite(normalizeGrant('Datastore.Allocate'))).toBe(true);
    expect(normalizeGrant({ scope: 'docker.containers', write: true })).toEqual({ name: 'docker.containers', access: 'write' });
    expect(normalizeGrant({ name: 'x', access: 'rw' }).access).toBe('write');
  });
});
