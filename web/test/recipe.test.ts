import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, mount } from '@vue/test-utils';
import { nextTick } from 'vue';
import IntegrationEditor from '../src/settings/IntegrationEditor.vue';
import RecipePanel from '../src/settings/RecipePanel.vue';
import { extraGrants, fills, gaps, inline, paramValid, paramValues, render, segments, slug, type Recipe } from '../src/settings/recipe';
import { setDraft } from '../src/onboarding/draft';
import { syncRoute } from '../src/lib/router';
import { resetIntegrations } from '../src/stores/integrations';
import type { JsonSchema } from '../src/lib/schema';
import { fakeFetch } from './fake-fetch';
import { harness } from './harness';

/** The shape GET /api/integrations/catalog serves (internal/integration/recipe.go). */
const PVE: Recipe = {
  title: 'A read-only API token',
  summary: 'A dedicated user, a role with only the audit privileges Cha Dash reads, and a privilege-separated token.',
  access: 'read-only',
  params: [
    { name: 'user', label: 'User', default: 'chadash@pve', pattern: '^[A-Za-z0-9][A-Za-z0-9._-]{0,62}@(pve|pam)$', help: 'The @pve realm keeps it out of Linux.' },
    { name: 'token', label: 'Token name', default: 'dash', pattern: '^[A-Za-z][A-Za-z0-9_-]{1,63}$' },
    {
      name: 'privs',
      label: 'Proxmox VE version',
      default: 'Sys.Audit VM.Audit VM.GuestAgent.Audit',
      options: [
        { label: '9.x', value: 'Sys.Audit VM.Audit VM.GuestAgent.Audit' },
        { label: '8.4', value: 'Sys.Audit VM.Audit' },
      ],
    },
    { name: 'url', label: 'Address', default: 'https://pve.lab.example:8006', pattern: '^https?://[A-Za-z0-9.-]+(:[0-9]{1,5})?$', from: 'config.url' },
  ],
  steps: [
    {
      title: 'Create the role',
      blocks: [
        { kind: 'text', text: 'As root, on **any node**. It holds `Sys.Audit` and the rest.' },
        { kind: 'code', lang: 'sh', code: 'pveum role add ChaDash --privs "{{privs}}"' },
      ],
    },
    {
      title: 'Create {{user}} and its token',
      blocks: [
        { kind: 'code', lang: 'sh', code: "pveum user add {{user}}\npveum acl modify / --tokens '{{user}}!{{token}}' --roles ChaDash\ndocker inspect --format '{{.Id}}' x" },
        { kind: 'list', ordered: true, items: ['Open {{url}}.', 'Paste the secret into **Token secret**.'] },
        { kind: 'code', lang: 'url', code: '{{url}}/#v1:0:18:4' },
      ],
    },
  ],
  notes: [{ kind: 'list', items: ['PVEAuditor works too.'] }],
  fills: { token_id: '{{user}}!{{token}}' },
  grants: ['Sys.Audit', 'VM.Audit', 'VM.GuestAgent.Audit'],
};

describe('recipe templates', () => {
  it('fill only values their pattern accepts', () => {
    const v = paramValues(PVE, { user: 'dash@pve', token: 'dash' }, { url: 'https://pve.lab.example:8006/' });
    expect(v).toEqual({ user: 'dash@pve', token: 'dash', privs: 'Sys.Audit VM.Audit VM.GuestAgent.Audit', url: 'https://pve.lab.example:8006' });
    expect(render("pveum acl modify / --tokens '{{user}}!{{token}}'", PVE, v)).toBe("pveum acl modify / --tokens 'dash@pve!dash'");
    // Not a parameter: left as it is.
    expect(render("docker inspect --format '{{.Id}}' {{token}}", PVE, v)).toBe("docker inspect --format '{{.Id}}' dash");

    // Whatever is typed, nothing that changes a command reaches it.
    for (const evil of ["x@pve'; rm -rf / #", 'a@pve $(id)', 'a@pve`id`', 'a b@pve', 'a@pve\nid', 'a@pve|id', '']) {
      const bad = paramValues(PVE, { user: evil, token: 'dash' }, {});
      expect(bad['user']).toBeUndefined();
      expect(render('pveum user add {{user}}', PVE, bad)).toBeUndefined();
      expect(gaps('pveum user add {{user}}', PVE, bad)).toEqual(['User']);
      expect(segments('pveum user add {{user}}', PVE, bad)).toEqual([
        { t: 'lit', v: 'pveum user add ' },
        { t: 'gap', name: 'user', label: 'User' },
      ]);
    }
    // A bound field that's empty or not an address is a gap, not a guess.
    expect(paramValues(PVE, {}, {})['url']).toBeUndefined();
    expect(paramValues(PVE, {}, { url: 'https://pve.lab.example/x?y=1;id' })['url']).toBeUndefined();
    // Options are by value, and nothing else gets in.
    expect(paramValid(PVE.params[2]!, 'Sys.Audit VM.Audit')).toBe(true);
    expect(paramValid(PVE.params[2]!, 'Sys.Modify')).toBe(false);
    // An unanchored or broken pattern accepts nothing.
    expect(paramValid({ name: 'x', label: 'X', default: 'a', pattern: '[a-z]+' }, 'a')).toBe(false);
    expect(paramValid({ name: 'x', label: 'X', default: 'a', pattern: '^([a-z]$' }, 'a')).toBe(false);
  });

  it('takes the instance’s name as a slug', () => {
    const r: Recipe = { ...PVE, params: [{ name: 'instance', label: 'Name', from: 'name', default: 'pve1', pattern: '^[a-z0-9][a-z0-9-]{0,62}$' }], fills: {} };
    expect(slug('  Proxmox VE (rack 2) ')).toBe('proxmox-ve-rack-2');
    expect(paramValues(r, {}, {}, 'Proxmox VE: pve2')).toEqual({ instance: 'proxmox-ve-pve2' });
    expect(render('--comment "Cha Dash: {{instance}}"', r, paramValues(r, {}, {}, '; rm -rf /'))).toBe('--comment "Cha Dash: rm-rf"');
    expect(paramValues(r, {}, {}, '')['instance']).toBeUndefined();
  });

  it('lets a form field pick a bound option (Unraid’s mode picks the scopes)', () => {
    const ro = 'info:read_any,docker:read_any';
    const ctl = 'info:read_any,docker:read_any,docker:update_any';
    const r: Recipe = {
      ...PVE,
      params: [
        {
          name: 'scopes',
          label: 'Scopes',
          from: 'config.mode',
          default: ro,
          options: [
            { label: 'Read-only', value: ro, when: 'read_only' },
            { label: 'Control', value: ctl, when: 'control' },
          ],
        },
      ],
      fills: {},
    };
    expect(paramValues(r, {}, { mode: 'control' })).toEqual({ scopes: ctl });
    expect(paramValues(r, {}, { mode: 'read_only' })).toEqual({ scopes: ro });
    // Not chosen yet: the default.
    expect(paramValues(r, {}, {})).toEqual({ scopes: ro });
    // A value no option names is a gap, and typing can't override the form.
    expect(paramValues(r, { scopes: ctl }, { mode: 'admin' })['scopes']).toBeUndefined();
    expect(render('?scopes={{scopes}}', r, paramValues(r, {}, { mode: 'control' }))).toBe(`?scopes=${ctl}`);
  });

  it('allows `code` and **bold**, never HTML', () => {
    expect(inline('Run `pveum` on **any node** <b>now</b>')).toEqual([
      { t: 'text', v: 'Run ' },
      { t: 'code', v: 'pveum' },
      { t: 'text', v: ' on ' },
      { t: 'strong', v: 'any node' },
      { t: 'text', v: ' <b>now</b>' },
    ]);
  });

  it('fills form fields and compares grants', () => {
    expect(fills(PVE, paramValues(PVE, { user: 'dash@pve' }, {}))).toEqual({ token_id: 'dash@pve!dash' });
    expect(fills(PVE, paramValues(PVE, { user: 'nope' }, {}))).toEqual({});
    expect(extraGrants(PVE, [{ name: 'Sys.Audit' }, { name: 'Sys.Modify' }, { name: 'VM.Audit' }])).toEqual(['Sys.Modify']);
  });
});

describe('the recipe panel', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    document.body.innerHTML = '';
  });

  it('generates the commands from the names, and copies only complete ones', async () => {
    const writeText = vi.fn(async () => undefined);
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } });
    vi.stubGlobal('isSecureContext', true);
    const w = mount(RecipePanel, { props: { recipe: PVE, form: {}, labels: { token_id: 'Token ID' } }, attachTo: document.body });
    expect(w.find('summary').text()).toContain('A read-only API token');
    expect(w.find('.rc-access').text()).toContain('Read-only');
    expect(w.findAll('.rc-steps > li')).toHaveLength(2);
    expect(w.find('.rc-steps h5').text()).toBe('Create the role');
    // Text markup becomes elements.
    expect(w.find('.rc-steps p b').text()).toBe('any node');
    expect(w.find('.rc-steps p code').text()).toBe('Sys.Audit');
    expect(w.findAll('.rc-steps > li')[1]!.find('h5').text()).toBe('Create chadash@pve and its token');

    const code = () => w.findAll('.rc-code pre').map((p) => p.text());
    expect(code()[1]).toContain("pveum acl modify / --tokens 'chadash@pve!dash' --roles ChaDash");
    expect(code()[1]).toContain("--format '{{.Id}}'");
    // The URL comes from the form: missing until it's filled in.
    expect(code()[2]).toBe('‹Address›/#v1:0:18:4');
    expect(w.find('.rc-wait').text()).toContain('Fill in Address in the form');
    const copyButtons = () => w.findAll('.rc-code-acts button');
    expect(copyButtons()[2]!.attributes('disabled')).toBeDefined();
    expect(w.find('.rc-code-acts a').exists()).toBe(false);

    await w.setProps({ form: { url: 'https://pve.lab.example:8006' } });
    expect(code()[2]).toBe('https://pve.lab.example:8006/#v1:0:18:4');
    const open = w.find('.rc-code-acts a');
    expect(open.attributes('href')).toBe('https://pve.lab.example:8006/#v1:0:18:4');
    expect(open.attributes('rel')).toBe('noopener noreferrer');

    // Change a name: every command follows.
    const user = w.find('.rc-gen input');
    await user.setValue('ops@pve');
    expect(code()[1]).toContain('pveum user add ops@pve');
    await copyButtons()[1]!.trigger('click');
    await flushPromises();
    expect(writeText).toHaveBeenCalledWith("pveum user add ops@pve\npveum acl modify / --tokens 'ops@pve!dash' --roles ChaDash\ndocker inspect --format '{{.Id}}' x");
    expect(copyButtons()[1]!.text()).toBe('Copied');

    // A name that could change the command never gets into it.
    await user.setValue('ops@pve; rm -rf /');
    expect(user.attributes('aria-invalid')).toBe('true');
    expect(w.find('.help.bad').text()).toContain('can’t go into a command');
    expect(code()[1]).toContain('pveum user add ‹User›');
    expect(code()[1]).not.toContain('rm -rf');
    expect(copyButtons()[1]!.attributes('disabled')).toBeDefined();

    // The version picks the privileges.
    await w.find('.rc-gen select').setValue('Sys.Audit VM.Audit');
    expect(code()[0]).toBe('pveum role add ChaDash --privs "Sys.Audit VM.Audit"');
  });

  it('carries what it determines into the form, and checks the test’s grants', async () => {
    const w = mount(RecipePanel, { props: { recipe: PVE, form: {}, labels: { token_id: 'Token ID' } } });
    expect(w.find('.rc-block .rel').text()).toContain('Token ID: chadash@pve!dash');
    await w.findAll('.rc-block button').find((b) => b.text() === 'Use in the form')!.trigger('click');
    expect(w.emitted('fill')).toEqual([[{ token_id: 'chadash@pve!dash' }]]);
    await w.setProps({ form: { token_id: 'chadash@pve!dash' } });
    expect(w.find('.rc-block .rel').text()).toContain('in the form');
    expect(w.findAll('.rc-block button').find((b) => b.text() === 'Use in the form')!.attributes('disabled')).toBeDefined();

    expect(w.findAll('.rc-chips .chip').map((c) => c.text())).toEqual(['Sys.Audit', 'VM.Audit', 'VM.GuestAgent.Audit']);
    await w.setProps({ test: { ok: true, grants: [{ name: 'Sys.Audit', access: 'read' }, { name: 'VM.Audit', access: 'read' }] } });
    expect(w.find('.rc-match').text()).toContain('nothing beyond these');
    await w.setProps({ test: { ok: true, grants: [{ name: 'Sys.Audit', access: 'read' }, { name: 'Sys.Modify', access: 'write' }] } });
    expect(w.find('.st-note.attn').text()).toContain('more than the recipe gives it: Sys.Modify');
  });

  it('says so plainly when only full access exists', () => {
    const w = mount(RecipePanel, {
      props: { recipe: { ...PVE, access: 'full', params: [], fills: {}, grants: [], steps: [{ title: 'Copy the key', blocks: [{ kind: 'text', text: 'Settings → General.' }] }] }, form: {}, open: false },
    });
    expect(w.find('.rc-access').text()).toContain('Full access only');
    expect(w.find('details').attributes('open')).toBeUndefined();
    expect(w.find('.rc-gen').exists()).toBe(false);
  });
});

const PVE_SCHEMA: JsonSchema = {
  type: 'object',
  required: ['url', 'token_id', 'token_secret'],
  properties: {
    url: { type: 'string', format: 'uri', title: 'URL' },
    token_id: { type: 'string', title: 'Token ID' },
    token_secret: { type: 'string', writeOnly: true, title: 'Token secret' },
  },
};

describe('the integration editor with a recipe', () => {
  beforeEach(() => {
    history.replaceState(null, '', '/settings/integrations/new/proxmox');
    syncRoute();
    resetIntegrations();
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    document.body.innerHTML = '';
  });

  function hub() {
    return fakeFetch({
      'GET /api/integrations/catalog': { status: 200, body: [{ id: 'proxmox', name: 'Proxmox VE', config_schema: PVE_SCHEMA, recipe: PVE }] },
      'GET /api/integrations/instances': { status: 200, body: [] },
      'POST /api/integrations/instances': { status: 201, body: { id: 'i_p' } },
    });
  }

  it('shows the recipe beside the form, takes its token ID, and starts from what discovery found', async () => {
    const f = hub();
    setDraft('proxmox', { name: 'Proxmox VE on pve1', config: { url: 'https://pve1.lab.example:8006' } });
    const h = harness();
    h.mountIn(IntegrationEditor, { integrationId: 'proxmox' });
    await flushPromises();
    await nextTick();
    expect(document.querySelector('.ed-grid.has-recipe .ed-aside details[open]')).not.toBeNull();
    expect((document.getElementById('f-name') as HTMLInputElement).value).toBe('Proxmox VE on pve1');
    expect((document.getElementById('f-url') as HTMLInputElement).value).toBe('https://pve1.lab.example:8006');
    // The URL in the form completes the recipe's commands.
    expect(document.querySelectorAll('.rc-code pre')[2]!.textContent).toBe('https://pve1.lab.example:8006/#v1:0:18:4');

    const use = [...document.querySelectorAll<HTMLButtonElement>('.rc-block button')].find((b) => b.textContent?.trim() === 'Use in the form')!;
    use.click();
    await nextTick();
    expect((document.getElementById('f-token_id') as HTMLInputElement).value).toBe('chadash@pve!dash');

    const secret = document.getElementById('f-token_secret') as HTMLInputElement;
    secret.value = 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee';
    secret.dispatchEvent(new Event('input'));
    await nextTick();
    [...document.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === 'Save')!.click();
    await flushPromises();
    expect(f.last('POST', '/api/integrations/instances')!.body).toEqual({
      integration: 'proxmox',
      name: 'Proxmox VE on pve1',
      config: { url: 'https://pve1.lab.example:8006', token_id: 'chadash@pve!dash' },
      secrets: { token_secret: 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee' },
    });
  });

  it('takes a draft only once', async () => {
    hub();
    const h = harness();
    h.mountIn(IntegrationEditor, { integrationId: 'proxmox' });
    await flushPromises();
    expect((document.getElementById('f-url') as HTMLInputElement).value).toBe('');
    expect((document.getElementById('f-name') as HTMLInputElement).value).toBe('Proxmox VE');
  });
});
