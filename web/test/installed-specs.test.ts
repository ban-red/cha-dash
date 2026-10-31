/**
 * Settings → Integrations → Installed specs (docs/plan/09-v0.3.md, "The
 * spec format v1alpha1"): check a spec, see what it reads and adds,
 * install it, turn its actions on, remove it.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { setDisplayTimeZone } from '../src/lib/format';
import { navigate } from '../src/lib/router';
import InstalledSpecs from '../src/settings/InstalledSpecs.vue';
import { axeViolations } from './axe';
import { fakeFetch } from './fake-fetch';
import { harness } from './harness';

beforeEach(() => {
  setDisplayTimeZone('UTC');
  navigate('/settings/integrations', { replace: true });
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

const home = () => ({ graph: { resources: {}, relations: {} }, metrics: { series: {} }, inbox: { now: '2026-10-31T12:05:00Z', incidents: {} } });

const SUMMARY = {
  id: 'myseerr',
  name: 'My Seerr',
  version: '0.2.0',
  secrets: ['api_key'],
  requests: [
    { name: 'status', method: 'GET', path: '/api/v1/status' },
    { name: 'pending', method: 'GET', path: '/api/v1/request', optional: true },
  ],
  actions: [{ id: 'myseerr.request.approve', title: 'Approve', tier: 'T1', method: 'POST', path: '/api/v1/request/{{ target.id }}/approve', body: ['note'], household: true }],
};
const INSTALLED = { id: 'myseerr', sha256: 'ab'.repeat(32), actions_on: false, installed_by: 'owner', installed_at: '2026-10-31T11:00:00Z', updated_at: '2026-10-31T11:00:00Z', instances: 1, summary: SUMMARY };

async function mountIt(list: unknown[] = []) {
  let specs = list;
  const hub = fakeFetch({
    'GET /api/specs/installed': () => ({ status: 200, body: { specs } }),
    'POST /api/specs/installed/preview': { status: 200, body: { summary: SUMMARY } },
    'POST /api/specs/installed': () => {
      specs = [{ ...INSTALLED, instances: 0 }];
      return { status: 200, body: specs[0] };
    },
    'PUT /api/specs/installed/myseerr/actions': { status: 200, body: { ...INSTALLED, actions_on: true } },
    'GET /api/integrations/catalog': { status: 200, body: { integrations: [] } },
  });
  const h = harness(home());
  await flushPromises();
  const w = h.mountIn(InstalledSpecs);
  await flushPromises();
  return { ...h, w, hub };
}

describe('Installed specs', () => {
  it('checks a spec, shows what it reads and adds, and installs it', async () => {
    const { w, hub } = await mountIt();
    expect(w.text()).toContain('None yet');
    await w.findAll('button').find((b) => b.text() === 'Add from a spec file')!.trigger('click');
    await w.find('textarea').setValue('apiVersion: chadash/v1alpha1\nkind: Integration\n');
    await w.findAll('button').find((b) => b.text() === 'Check')!.trigger('click');
    await flushPromises();
    const pv = w.find('.is-preview');
    expect(pv.text()).toContain('GET /api/v1/status');
    expect(pv.text()).toContain('optional');
    expect(pv.text()).toContain('adds 1 action, off until you turn them on');
    expect(pv.text()).toContain('Approve T1 · POST /api/v1/request/{{ target.id }}/approve · sends note · household members can run it');
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);
    await w.findAll('button').find((b) => b.text() === 'Install')!.trigger('click');
    await flushPromises();
    expect(hub.last('POST', '/api/specs/installed')?.body).toMatchObject({ yaml: 'apiVersion: chadash/v1alpha1\nkind: Integration\n', replace: false });
    expect(w.find('.is-row').text()).toContain('Actions off: Approve (T1, POST');
  });

  it('turns actions on, and keeps Remove off while it has instances', async () => {
    const { w, hub } = await mountIt([INSTALLED]);
    const remove = w.findAll('button').find((b) => b.text() === 'Remove')!;
    expect(remove.attributes('disabled') ?? remove.attributes('aria-disabled')).toBeDefined();
    await w.findAll('button').find((b) => b.text() === 'Turn actions on')!.trigger('click');
    await flushPromises();
    expect(hub.last('PUT', '/api/specs/installed/myseerr/actions')?.body).toEqual({ on: true, sha256: INSTALLED.sha256 });
    expect(w.find('.is-row').text()).toContain('Actions on');
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);
  });

  it('shows why a spec doesn’t load, and offers Replace for one installed', async () => {
    const { w } = await mountIt();
    await w.findAll('button').find((b) => b.text() === 'Add from a spec file')!.trigger('click');
    await w.find('textarea').setValue('x: 1');
    fakeFetch({
      'POST /api/specs/installed/preview': {
        status: 400,
        body: { error: { code: 'invalid_spec', message: 'The spec doesn’t load: at /: missing property apiVersion', details: ['at /: missing property apiVersion', 'at /meta: missing property id'] } },
      },
    });
    await w.findAll('button').find((b) => b.text() === 'Check')!.trigger('click');
    await flushPromises();
    expect(w.find('[role="alert"]').text()).toContain('missing property apiVersion');
    expect(w.find('[role="alert"]').text()).toContain('at /meta: missing property id');

    fakeFetch({
      'POST /api/specs/installed/preview': {
        status: 409,
        body: { error: { code: 'exists', message: 'installed already' }, preview: { summary: { ...SUMMARY, version: '0.3.0' }, replacing: INSTALLED } },
      },
    });
    await w.find('textarea').setValue('apiVersion: chadash/v1alpha1');
    await w.findAll('button').find((b) => b.text() === 'Check')!.trigger('click');
    await flushPromises();
    expect(w.text()).toContain('is installed already (0.2.0). Replace it with 0.3.0');
    expect(w.findAll('button').some((b) => b.text() === 'Replace')).toBe(true);
  });
});
