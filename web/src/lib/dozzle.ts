/**
 * Dozzle deep links: when an admin has set Dozzle's address (Settings →
 * Integrations → Dozzle, `GET /api/settings/dozzle`), a container's sheet
 * links to its logs there. Cha Dash never talks to Dozzle; the browser
 * follows the link.
 *
 * Dozzle's routes put a container at `<base>/container/<id>`, by its short
 * (12-character) Docker ID, which the Docker integration publishes as
 * `attrs.container_id`. Checked against Dozzle v11.4.0's source
 * (github.com/amir20/dozzle): the page is `assets/pages/container/[id].vue`
 * (file-based routes), the backend serves the SPA for any path under
 * `--base` (`internal/web/routes.go`), and a container's `id` is
 * `c.ID[:12]` (`internal/container/docker/client.go`). The page looks the ID
 * up in one map of every host's containers (`assets/stores/container.ts`,
 * `currentContainer`), so the same link works for a container on a remote
 * host or an agent: no host goes in the URL. (`/show?name=&host=` also
 * exists, by name, but a name can match a newer container.)
 */
import { ref } from 'vue';
import { api } from './api';

/** Dozzle's base address, without a trailing slash; '' when unset or unknown. */
export const dozzleBase = ref('');

let asked: Promise<void> | undefined;

/** Reads the address once per page (again with `again`); failures leave it unset. */
export function loadDozzle(again = false): Promise<void> {
  if (asked && !again) return asked;
  asked = api<{ url?: unknown }>('/api/settings/dozzle')
    .then((s) => {
      dozzleBase.value = typeof s?.url === 'string' ? s.url : '';
    })
    .catch(() => {
      dozzleBase.value = '';
    });
  return asked;
}

const CONTAINER_ID = /^[0-9a-f]{12,64}$/;

/** The link to a container's logs in Dozzle, or undefined without an address or an ID. */
export function dozzleLink(base: string, containerId: unknown): string | undefined {
  if (!/^https?:\/\//.test(base) || typeof containerId !== 'string' || !CONTAINER_ID.test(containerId)) return undefined;
  return `${base.replace(/\/+$/, '')}/container/${containerId.slice(0, 12)}`;
}
