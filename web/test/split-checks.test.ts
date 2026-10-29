import { afterEach, describe, expect, it } from 'vitest';
import { mount } from '@vue/test-utils';
import SplitChecks from '../src/settings/SplitChecks.vue';
import { takeDraft } from '../src/onboarding/draft';
import { integrations } from '../src/stores/integrations';
import { route } from '../src/lib/router';

const entry = (id: string, name: string) => ({ id, name, config_schema: { type: 'object' as const, properties: {} } });

afterEach(() => {
  integrations.catalog = [];
});

describe('SplitChecks', () => {
  it('offers an integration for the targets that name one, and fills the add form', async () => {
    integrations.catalog = [entry('unraid', 'Unraid'), entry('linkding', 'Linkding'), entry('checks', 'Checks'), entry('proxmox', 'Proxmox')];
    const w = mount(SplitChecks, {
      props: {
        targets: [
          { name: 'Jupiter Unraid', kind: 'http', url: 'http://10.0.0.5', tls: { mode: 'tofu' } },
          { name: 'JupiterNasCockpit', kind: 'http', url: 'https://nas.lan:9090/' },
          { name: 'Linkding', kind: 'http', url: 'https://links.example' },
          { name: 'Proxmox Backup', kind: 'http', url: 'https://pbs.lan:8007' },
          { name: 'Unraid SSH', kind: 'tcp', host: 'x', port: 22 },
        ],
      },
    });
    const rows = w.findAll('.rel li');
    expect(rows.map((r) => r.find('button').text())).toEqual(['Set up Unraid', 'Set up Linkding']);

    await rows[0]!.find('button').trigger('click');
    expect(route.value.path).toBe('/settings/integrations/new/unraid');
    expect(takeDraft('unraid')).toEqual({ name: 'Jupiter Unraid', config: { url: 'http://10.0.0.5', tls: { mode: 'tofu' } } });
  });

  it('says so when nothing matches', () => {
    integrations.catalog = [entry('unraid', 'Unraid')];
    const w = mount(SplitChecks, { props: { targets: [{ name: 'Cockpit', kind: 'http', url: 'https://x' }] } });
    expect(w.find('.rel').exists()).toBe(false);
    expect(w.text()).toContain('None of these targets');
  });
});
