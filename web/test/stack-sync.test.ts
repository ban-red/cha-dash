/**
 * Git sync and deploy webhooks (wave 12): Follow git for one stack (save,
 * write-only secret, Test and pinning the SSH host key, step-up, check now,
 * resume, unfollow), the read-only editor of a synced stack, and the
 * webhooks list (the secret once, rotate, delete, empty and error states),
 * with axe on each. The hub's side is faked to the wave 12 API.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { nextTick } from 'vue';
import StackSync from '../src/settings/StackSync.vue';
import StackHooks from '../src/settings/StackHooks.vue';
import StacksPane from '../src/settings/StacksPane.vue';
import { setDisplayTimeZone } from '../src/lib/format';
import { navigate } from '../src/lib/router';
import type { StackDetail } from '../src/settings/stacks';
import type { HookView, SyncView } from '../src/settings/sync';
import { axeViolations } from './axe';
import { refuse } from './fake-engine';
import { fakeFetch } from './fake-fetch';
import { docs, withIncidents } from './fixtures';
import { harness } from './harness';

beforeEach(() => {
  setDisplayTimeZone('UTC');
  navigate('/', { replace: true });
});
const mounted: { unmount: () => void }[] = [];
afterEach(() => {
  for (const w of mounted.splice(0)) w.unmount();
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

async function settle(): Promise<void> {
  await vi.dynamicImportSettled();
  await flushPromises();
  await nextTick();
}

function open(path = '/settings/stacks') {
  navigate(path, { replace: true });
  const h = harness(docs(withIncidents));
  const mountIn: typeof h.mountIn = (c, p) => {
    const w = h.mountIn(c, p);
    mounted.push(w);
    return w;
  };
  return { ...h, mountIn };
}

const URL_ = 'https://git.lab.example/ops/stacks.git';
const SYNC_PATH = '/api/stacks/local/media/sync';

function synced(over: Partial<SyncView> = {}): SyncView {
  return {
    id: 'syn_abc',
    node: 'local',
    stack: 'media',
    url: URL_,
    branch: 'main',
    path: 'media/compose.yaml',
    auth: { kind: 'https', username: 'deploy', secret_set: true },
    host_key: null,
    mode: 'ask',
    poll_seconds: 300,
    secrets_allow: ['db_password'],
    owner: { id: 'u1', username: 'owner' },
    state: 'active',
    state_reason: '',
    last_seen_sha: '1'.repeat(40),
    last_deployed_sha: '2'.repeat(40),
    rejected_sha: '',
    last_error: '',
    running: false,
    hook: { id: 'whk_1', path: '/api/hooks/whk_1', sender: 'github', last_used_at: '2026-10-29T12:00:00Z' },
    created_at: '2026-10-28T12:00:00Z',
    updated_at: '2026-10-29T12:00:00Z',
    ...over,
  };
}

const HOOK: HookView = {
  id: 'whk_1',
  url: 'https://git.lab.example/ops/stacks',
  branch: 'main',
  sender: 'github',
  path: '/api/hooks/whk_1',
  created_at: '2026-10-28T12:00:00Z',
  created_by: 'owner',
  last_used_at: '2026-10-29T12:00:00Z',
  failures: 2,
  syncs: [{ node: 'local', stack: 'media', mode: 'ask', state: 'active' }],
};

const KEY = 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeHostKeyForTestsOnly';

async function stepUpWhenAsked(h: ReturnType<typeof open>): Promise<void> {
  for (let i = 0; i < 20 && !h.ui.state.stepUp; i++) await settle();
  expect(h.ui.state.stepUp).toBeDefined();
  h.ui.stepUpDone(true);
  await settle();
}

describe('Follow git', () => {
  it('leaves “Checking now…” once the hub’s check ends, and says the sync changed', async () => {
    vi.useFakeTimers();
    try {
      let gets = 0;
      fakeFetch({ [`GET ${SYNC_PATH}`]: () => ({ status: 200, body: { sync: synced({ running: ++gets < 3, last_seen_sha: gets < 3 ? '1'.repeat(40) : '3'.repeat(40) }) } }) });
      const h = open();
      const w = h.mountIn(StackSync, { node: 'local', stack: 'media', secretNames: [] });
      await settle();
      expect(w.text()).toContain('Checking now…');
      expect(w.findComponent(StackSync).emitted('changed')).toBeUndefined();
      await vi.advanceTimersByTimeAsync(2100);
      await settle();
      expect(w.text()).not.toContain('Checking now…');
      expect(gets).toBe(3);
      const changed = w.findComponent(StackSync).emitted('changed') as [SyncView][];
      expect(changed).toHaveLength(1);
      expect(changed[0]![0].last_seen_sha).toBe('3'.repeat(40));
      // Done: no more reads.
      await vi.advanceTimersByTimeAsync(5000);
      expect(gets).toBe(3);
    } finally {
      vi.useRealTimers();
    }
  });

  it('starts empty, then follows: the PUT carries the form, the secret goes once, and step-up is asked for deploy on push', async () => {
    let puts = 0;
    const f = fakeFetch({
      [`GET ${SYNC_PATH}`]: { status: 200, body: { sync: null } },
      [`PUT ${SYNC_PATH}`]: (b) => {
        puts++;
        if (puts === 1) return refuse(403, 'step_up_required', 'Confirm it’s you first.');
        const req = b as { url: string; mode: 'ask' | 'deploy' };
        return { status: 201, body: { sync: synced({ url: req.url, mode: req.mode }) } };
      },
    });
    const h = open();
    const w = h.mountIn(StackSync, { node: 'local', stack: 'media', secretNames: ['db_password', 'api_key'] });
    await settle();
    expect(w.text()).toContain('Keep this stack’s compose file in your own git.');
    expect(await axeViolations()).toEqual([]);

    await w.find('button').trigger('click'); // Follow git
    await settle();
    expect((w.find('input[type="url"]').element as HTMLInputElement).value).toBe('');
    // Nothing typed: the form says what's missing and sends nothing.
    await w.find('form').trigger('submit');
    await settle();
    expect(w.text()).toContain('Give the remote’s address.');
    expect(f.calls.filter((c) => c.method === 'PUT')).toHaveLength(0);

    await w.find('input[type="url"]').setValue(URL_);
    const path = w.findAll('input[type="text"]').find((i) => (i.element as HTMLInputElement).value === 'media/compose.yaml');
    expect(path).toBeDefined();
    await w.find('input[value="https"]').setValue(true);
    await settle();
    const textInputs = w.findAll('fieldset input[type="text"]');
    await textInputs[0]!.setValue('deploy');
    await w.find('input[type="password"]').setValue('FAKE-token-123');
    await w.find('input[value="deploy"]').setValue(true);
    await settle();
    expect(w.text()).toContain('A push deploys without anyone approving it; a bad commit still rolls back.');
    await w.find('input[value="api_key"]').setValue(true);
    expect(await axeViolations()).toEqual([]);
    await w.find('form').trigger('submit');
    await stepUpWhenAsked(h);

    const put = f.last('PUT', SYNC_PATH)!;
    expect(put.body).toEqual({
      url: URL_,
      branch: 'main',
      path: 'media/compose.yaml',
      auth: { kind: 'https', username: 'deploy', secret: 'FAKE-token-123' },
      mode: 'deploy',
      poll_seconds: 300,
      secrets_allow: ['api_key'],
    });
    expect(puts).toBe(2);
    expect(w.text()).toContain('following');
    expect(w.text()).toContain('deploy on push');
    expect(w.find('form').exists()).toBe(false);
    expect(document.body.innerHTML).not.toContain('FAKE-token-123');
    expect((w.findComponent(StackSync).emitted('changed') as unknown[][]).at(-1)![0]).toMatchObject({ id: 'syn_abc' });
  });

  it('keeps the saved secret when the box is left empty', async () => {
    const f = fakeFetch({
      [`GET ${SYNC_PATH}`]: { status: 200, body: { sync: synced() } },
      [`PUT ${SYNC_PATH}`]: { status: 200, body: { sync: synced({ poll_seconds: 600 }) } },
    });
    const h = open();
    const w = h.mountIn(StackSync, { node: 'local', stack: 'media', secretNames: ['db_password'] });
    await settle();
    expect(w.text()).toContain('media/compose.yaml');
    expect(w.text()).toContain('1111111');
    expect(w.text()).toContain('2222222');
    expect(w.text()).toContain('http://hub.test/api/hooks/whk_1'.replace('http://hub.test', location.origin));
    const change = w.findAll('button').find((b) => b.text() === 'Change')!;
    await change.trigger('click');
    await settle();
    expect(w.text()).toContain('Saved. Leave it empty to keep it');
    await w.find('input[type="number"]').setValue(10);
    await w.find('form').trigger('submit');
    await settle();
    const body = f.last('PUT', SYNC_PATH)!.body as { auth: Record<string, unknown>; poll_seconds: number; secrets_allow: string[] };
    expect(body.auth).toEqual({ kind: 'https', username: 'deploy' });
    expect(body.poll_seconds).toBe(600);
    expect(body.secrets_allow).toEqual(['db_password']);
  });

  it('tests an SSH remote, shows its host key, and pinning it fills host_key', async () => {
    const f = fakeFetch({
      [`GET ${SYNC_PATH}`]: { status: 200, body: { sync: null } },
      [`POST ${SYNC_PATH}/check`]: { status: 200, body: { sha: 'f'.repeat(40), host_key: KEY } },
      [`PUT ${SYNC_PATH}`]: (b) => ({ status: 201, body: { sync: synced({ auth: { kind: 'ssh', username: '', secret_set: true }, host_key: (b as { host_key: string }).host_key }) } }),
    });
    const h = open();
    const w = h.mountIn(StackSync, { node: 'local', stack: 'media' });
    await settle();
    await w.find('button').trigger('click');
    await settle();
    await w.find('input[type="url"]').setValue('ssh://git.lab.example/ops/stacks.git');
    await w.find('input[value="ssh"]').setValue(true);
    await settle();
    await w.find('textarea').setValue('-----BEGIN OPENSSH PRIVATE KEY-----\nFAKE\n-----END OPENSSH PRIVATE KEY-----\n');
    // No pinned key: saving refuses.
    await w.find('form').trigger('submit');
    await settle();
    expect(w.text()).toContain('Test the remote, then pin the host key it shows.');

    await w.findAll('button').find((b) => b.text() === 'Test')!.trigger('click');
    await settle();
    expect(f.last('POST', `${SYNC_PATH}/check`)!.body).toEqual({
      url: 'ssh://git.lab.example/ops/stacks.git',
      branch: 'main',
      auth: { kind: 'ssh', secret: '-----BEGIN OPENSSH PRIVATE KEY-----\nFAKE\n-----END OPENSSH PRIVATE KEY-----\n' },
    });
    expect(w.text()).toContain('main is at fffffff');
    expect(w.text()).toContain(KEY);
    expect(await axeViolations()).toEqual([]);
    await w.findAll('button').find((b) => b.text() === 'Pin this host key')!.trigger('click');
    await settle();
    expect(w.findAll('button').some((b) => b.text() === 'Pin this host key')).toBe(false);
    await w.find('textarea').setValue('-----BEGIN OPENSSH PRIVATE KEY-----\nFAKE\n-----END OPENSSH PRIVATE KEY-----\n');
    await w.find('form').trigger('submit');
    await settle();
    expect((f.last('PUT', SYNC_PATH)!.body as { host_key: string }).host_key).toBe(KEY);
  });

  it('says why a test failed', async () => {
    fakeFetch({
      [`GET ${SYNC_PATH}`]: { status: 200, body: { sync: null } },
      [`POST ${SYNC_PATH}/check`]: refuse(403, 'forbidden', 'This node’s policy doesn’t allow git.lab.example (stacks.remotes).'),
    });
    const h = open();
    const w = h.mountIn(StackSync, { node: 'local', stack: 'media' });
    await settle();
    await w.find('button').trigger('click');
    await settle();
    await w.find('input[type="url"]').setValue(URL_);
    await w.findAll('button').find((b) => b.text() === 'Test')!.trigger('click');
    await settle();
    expect(w.find('[role="alert"]').text()).toContain('stacks.remotes');
  });

  it('checks now, resumes a held sync with a step-up, and unfollows after confirming', async () => {
    let resumes = 0;
    const f = fakeFetch({
      [`GET ${SYNC_PATH}`]: { status: 200, body: { sync: synced({ state: 'held', state_reason: 'Held at 3333333 after owner rolled back.' }) } },
      [`POST ${SYNC_PATH}/resume`]: () => (++resumes === 1 ? refuse(403, 'step_up_required', 'Confirm it’s you first.') : { status: 200, body: { sync: synced() } }),
      [`POST ${SYNC_PATH}/now`]: { status: 202, body: { queued: true } },
      [`DELETE ${SYNC_PATH}`]: { status: 204 },
    });
    const h = open();
    const w = h.mountIn(StackSync, { node: 'local', stack: 'media' });
    await settle();
    expect(w.text()).toContain('held');
    expect(w.text()).toContain('Held at 3333333 after owner rolled back.');
    expect(w.findAll('button').some((b) => b.text() === 'Check now')).toBe(false);
    expect(await axeViolations()).toEqual([]);
    await w.findAll('button').find((b) => b.text() === 'Resume')!.trigger('click');
    await stepUpWhenAsked(h);
    expect(resumes).toBe(2);
    expect(w.text()).toContain('following');

    await w.findAll('button').find((b) => b.text() === 'Check now')!.trigger('click');
    await settle();
    expect(f.last('POST', `${SYNC_PATH}/now`)).toBeDefined();
    expect(h.ui.state.toasts.at(-1)!.text).toBe('Checking main now.');

    await w.findAll('button').find((b) => b.text() === 'Unfollow')!.trigger('click');
    await settle();
    expect(w.find('[role="group"]').text()).toContain('The files and the history stay');
    expect(document.activeElement?.textContent?.trim()).toBe('Keep following');
    await w.find('[role="group"]').findAll('button').find((b) => b.text() === 'Unfollow')!.trigger('click');
    await settle();
    expect(f.last('DELETE', SYNC_PATH)).toBeDefined();
    expect(w.text()).toContain('Keep this stack’s compose file in your own git.');
    expect((w.findComponent(StackSync).emitted('changed') as unknown[][]).at(-1)![0]).toBeNull();
  });

  it('shows the loading error with a retry, and that a protected stack can’t follow', async () => {
    fakeFetch({ [`GET ${SYNC_PATH}`]: refuse(503, 'node_unavailable', 'The node local isn’t connected.') });
    const h = open();
    const w = h.mountIn(StackSync, { node: 'local', stack: 'media' });
    await settle();
    expect(w.find('[role="alert"]').text()).toContain('The node local isn’t connected.');
    expect(w.find('[role="alert"] button').text()).toBe('Try again');

    document.body.innerHTML = '';
    fakeFetch({ [`GET ${SYNC_PATH}`]: { status: 200, body: { sync: null } } });
    const h2 = open();
    const w2 = h2.mountIn(StackSync, { node: 'local', stack: 'media', protected: true });
    await settle();
    expect(w2.text()).toContain('A protected stack can’t follow git.');
    expect(w2.findAll('button').some((b) => b.text() === 'Follow git')).toBe(false);
  });
});

describe('The editor of a synced stack', () => {
  const detail = (over: Partial<StackDetail> = {}): StackDetail => ({
    node: 'local',
    name: 'media',
    path: '/opt/stacks/media',
    file: 'compose.yaml',
    location: 'root',
    managed: true,
    protected: false,
    protected_reason: null,
    drift: false,
    status: 'running',
    content: 'services:\n  web:\n    image: nginx:1.27\n',
    env: { present: false, keys: [] },
    meta: {},
    services: [],
    history: [],
    draft: { content: 'services:\n  web:\n    image: nginx:1.28\n', revision: 2, updated_at: '2026-10-29T12:00:00Z', updated_by: 'git sync', source: 'git:' + 'a'.repeat(40) },
    secrets: [],
    sync: synced(),
    ...over,
  });

  it('is read-only, says where to edit, and marks a draft from git', async () => {
    fakeFetch({
      'GET /api/stacks/local/media': { status: 200, body: detail() },
      [`GET ${SYNC_PATH}`]: { status: 200, body: { sync: synced() } },
      'POST /api/stacks/local/media/validate': { status: 200, body: { ok: true, messages: [], secrets: [], plan: null, diff: [] } },
    });
    const h = open('/settings/stacks/local/media');
    const w = h.mountIn(StacksPane);
    await settle();
    await settle();
    expect(w.find('textarea').attributes('readonly')).toBeDefined();
    expect(w.find('.sk-synced').text()).toContain(`Edit media/compose.yaml on main in ${URL_}.`);
    expect(w.text()).toContain('From git, commit aaaaaaa');
    expect(w.findAll('button').some((b) => b.text().startsWith('Save draft'))).toBe(false);
    expect(w.findAll('button').some((b) => b.text() === 'Discard draft')).toBe(false);
    expect(w.findAll('button').some((b) => b.text().startsWith('Deploy'))).toBe(true);
  });
});

describe('Deploy webhooks', () => {
  it('lists hooks with their URL, syncs, last use and refusals; says when there are none; says when they can’t load', async () => {
    fakeFetch({ 'GET /api/stacks/hooks': { status: 200, body: { hooks: [HOOK] } } });
    const h = open();
    const w = h.mountIn(StackHooks);
    await settle();
    const row = w.find('.skh-row');
    expect(row.text()).toContain('https://git.lab.example/ops/stacks');
    expect(row.text()).toContain('GitHub');
    expect(row.text()).toContain(`${location.origin}/api/hooks/whk_1`);
    expect(row.text()).toContain('media on local (draft and ask)');
    expect(row.text()).toContain('2 refused requests');
    expect(await axeViolations()).toEqual([]);

    document.body.innerHTML = '';
    fakeFetch({ 'GET /api/stacks/hooks': { status: 200, body: { hooks: [] } } });
    const w2 = open().mountIn(StackHooks);
    await settle();
    expect(w2.text()).toContain('No webhooks. Syncs poll every few minutes; a webhook makes them follow a push at once.');
    expect(await axeViolations()).toEqual([]);

    document.body.innerHTML = '';
    fakeFetch({ 'GET /api/stacks/hooks': refuse(500, 'internal', 'internal error') });
    const w3 = open().mountIn(StackHooks);
    await settle();
    expect(w3.find('[role="alert"]').text()).toContain('internal error');
  });

  it('makes a hook with deploy on push after a step-up, and shows the secret once', async () => {
    let posts = 0;
    const f = fakeFetch({
      'GET /api/stacks/hooks': { status: 200, body: { hooks: [] } },
      'POST /api/stacks/hooks': () =>
        ++posts === 1
          ? refuse(403, 'step_up_required', 'Confirm it’s you first.')
          : {
              status: 201,
              body: {
                hook: HOOK,
                secret: 'FAKE-hook-secret-0123456789',
                switched: 1,
                not_switched: [{ node: 'pve-b', stack: 'books', mode: 'ask', state: 'active' }],
              },
            },
    });
    const h = open();
    const w = h.mountIn(StackHooks);
    await settle();
    await w.findAll('button').find((b) => b.text() === 'New webhook')!.trigger('click');
    await settle();
    await w.find('input[type="url"]').setValue(URL_);
    await w.find('select').setValue('gitlab');
    await w.find('input[type="checkbox"]').setValue(true);
    expect(await axeViolations()).toEqual([]);
    await w.find('form').trigger('submit');
    await stepUpWhenAsked(h);
    expect(f.last('POST', '/api/stacks/hooks')!.body).toEqual({ url: URL_, branch: 'main', sender: 'gitlab', deploy_on_push: true });
    const fresh = w.find('.skh-fresh');
    expect(fresh.exists()).toBe(true);
    expect((fresh.findAll('input').at(1)!.element as HTMLInputElement).value).toBe('FAKE-hook-secret-0123456789');
    expect(fresh.text()).toContain('This is the only time Cha Dash shows it.');
    expect(h.ui.state.toasts.at(-1)!.text).toBe('Switched 1 sync to deploy on push.');
    expect(fresh.find('.skh-not-switched').text()).toContain('Not switched to deploy on push: books on pve-b. They belong to other admins');
    expect(await axeViolations()).toEqual([]);
    await fresh.findAll('button').find((b) => b.text() === 'I’ve saved it')!.trigger('click');
    await settle();
    expect(w.find('.skh-fresh').exists()).toBe(false);
    expect(document.body.innerHTML).not.toContain('FAKE-hook-secret-0123456789');
  });

  it('rotates after confirming, showing the new secret once, and deletes', async () => {
    const f = fakeFetch({
      'GET /api/stacks/hooks': { status: 200, body: { hooks: [HOOK] } },
      'POST /api/stacks/hooks/whk_1/rotate': { status: 200, body: { hook: HOOK, secret: 'FAKE-rotated-secret-9876' } },
      'DELETE /api/stacks/hooks/whk_1': { status: 204 },
    });
    const h = open();
    const w = h.mountIn(StackHooks);
    await settle();
    await w.find('button[aria-label^="Rotate"]').trigger('click');
    await settle();
    expect(w.find('.skh-confirm').text()).toContain('The old one stops working at once');
    expect(document.activeElement?.textContent?.trim()).toBe('Cancel');
    await w.find('.skh-confirm').findAll('button').find((b) => b.text() === 'Rotate')!.trigger('click');
    await settle();
    expect(f.last('POST', '/api/stacks/hooks/whk_1/rotate')).toBeDefined();
    expect(w.find('.skh-fresh').text()).toContain('Copy the new secret now');
    expect(w.find('.skh-fresh').text()).toContain('The old secret has stopped working');

    await w.find('button[aria-label^="Delete"]').trigger('click');
    await settle();
    expect(w.find('.skh-confirm').text()).toContain('keep polling');
    await w.find('.skh-confirm').findAll('button').find((b) => b.text() === 'Delete')!.trigger('click');
    await settle();
    expect(f.last('DELETE', '/api/stacks/hooks/whk_1')).toBeDefined();
    expect(w.find('.skh-fresh').exists()).toBe(false);
  });
});

