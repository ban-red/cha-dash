<script setup lang="ts">
/**
 * On a checks instance: the targets that name an app Cha Dash has an
 * integration for ("Jupiter Unraid", "Linkding"). A check only says whether
 * a page answers; the integration reads the service itself. "Set up" opens
 * the add form with the target's name and address filled in, so only the
 * credential is left. An API key in the target's URL (`?apikey=…`) moves to
 * the form's secret field and out of the address. The check stays until you remove its target.
 */
import { computed } from 'vue';
import { navigate } from '../lib/router';
import { setDraft } from '../onboarding/draft';
import { integrations } from '../stores/integrations';
import UiButton from '../ui/Button.vue';

const { targets } = defineProps<{ targets: readonly unknown[] }>();

/** Integrations that read something other than one app at a URL. */
const NOT_APPS = new Set(['checks', 'docker', 'weather']);

/** Query parameters a service's API key usually travels in. */
const KEY_PARAMS = ['apikey', 'api_key', 'api-key', 'token', 'access_token', 'key'];

/** The URL without an API key in its query, and the key it carried. */
function splitKey(raw: string): { url: string; key?: string } {
  let u: URL;
  try {
    u = new URL(raw);
  } catch {
    return { url: raw };
  }
  const name = [...u.searchParams.keys()].find((k) => KEY_PARAMS.includes(k.toLowerCase()));
  const key = name ? u.searchParams.get(name) : null;
  if (!name || !key) return { url: raw };
  u.searchParams.delete(name);
  return { url: u.toString(), key };
}

const words = (s: string) => s.toLowerCase().split(/[^a-z0-9]+/).filter(Boolean);
const squash = (s: string) => s.toLowerCase().replace(/[^a-z0-9]/g, '');

interface Row {
  index: number;
  name: string;
  url: string;
  tls?: unknown;
  integration: string;
  label: string;
}

const rows = computed<Row[]>(() => {
  const out: Row[] = [];
  targets.forEach((t, index) => {
    const o = (t ?? {}) as Record<string, unknown>;
    const name = typeof o['name'] === 'string' ? o['name'] : '';
    const url = o['kind'] === 'http' && typeof o['url'] === 'string' ? o['url'] : '';
    if (!name || !url) return;
    // The whole name, or one of its words: "Jupiter Unraid" is Unraid, "Proxmox Backup" isn't Proxmox's.
    const ws = new Set([squash(name), ...words(name)]);
    const hit = integrations.catalog.find((c) => !NOT_APPS.has(c.id) && (ws.has(c.id) || ws.has(squash(c.name))));
    if (hit && !(squash(name) !== hit.id && words(name).length > 1 && words(name).includes('backup') && hit.id === 'proxmox')) {
      out.push({ index, name, url, tls: o['tls'], integration: hit.id, label: hit.name });
    }
  });
  return out;
});

function setUp(r: Row): void {
  const { url, key } = splitKey(r.url);
  const config: Record<string, unknown> = { url };
  const tls = r.tls as { mode?: unknown } | undefined;
  // A check may skip certificate checks; a connection that holds a key pins the certificate instead.
  if (tls && typeof tls === 'object' && typeof tls.mode === 'string') config['tls'] = { mode: tls.mode === 'insecure' ? 'tofu' : tls.mode };
  setDraft(r.integration, key ? { name: r.name, config, key } : { name: r.name, config });
  navigate(`/settings/integrations/new/${encodeURIComponent(r.integration)}`);
}
</script>

<template>
  <section class="ed-split" aria-labelledby="split-h">
    <h4 id="split-h" class="caps">Give a service its own integration</h4>
    <p class="help">A check only says whether a page answers. An integration reads the service itself. The check stays until you remove its target above.</p>
    <ul v-if="rows.length" class="rel">
      <li v-for="r in rows" :key="r.index">
        <span>
          <b>{{ r.name }}</b> looks like {{ r.label }}
          <span class="help">{{ r.url }}</span>
        </span>
        <UiButton size="sm" @click="setUp(r)">Set up {{ r.label }}</UiButton>
      </li>
    </ul>
    <p v-else class="help">None of these targets look like an app Cha Dash has an integration for.</p>
  </section>
</template>
