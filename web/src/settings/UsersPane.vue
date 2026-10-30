<script setup lang="ts">
/**
 * Settings → Users (owner; admins manage everyone but the owner):
 *
 *   GET  /api/users                          → [{id, username, role, disabled, has_passkey, last_seen_at}]
 *   POST /api/users                          {username, password, role}
 *   PUT  /api/users/{id}                     {role?} | {disabled?}
 *   POST /api/users/{id}/transfer-ownership  T3: type the name, then step-up
 *
 * Banned addresses (BannedAddresses.vue) follow the people.
 *
 * There is exactly one owner. The owner can't be demoted, disabled or
 * deleted, only hand ownership to someone else. Users are disabled, never
 * deleted, so the audit log keeps pointing at someone. What the viewer's role
 * can't do isn't offered; the hub enforces it either way, and any change it
 * wants a step-up for runs the step-up dialog and tries again.
 *
 * A role select only marks the choice; its Save sends it, so arrowing
 * through the roles never saves (WCAG 3.2.2). Buttons that may hold focus
 * are aria-disabled rather than disabled while they work, so focus stays on
 * them and the step-up dialog hands it back there.
 *
 * Its own chunk, loaded with the pane.
 */
import SettingsHelp from './SettingsHelp.vue';
import { computed, nextTick, onMounted, reactive, ref, useId } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import BannedAddresses from './BannedAddresses.vue';
import { ago, absolute } from '../lib/format';
import { api } from '../lib/api';
import { hubWords } from '../lib/actions';
import { asRole, assignableRoles, ROLE_WORDS, type Role } from '../model/roles';
import { StepUpCancelled } from '../stores/actions';
import { MIN_PASSWORD, passwordLength, USERNAME_RE, useSession, type User } from '../stores/session';
import { focusFirst } from '../lib/focus';
import { useUi } from '../stores/ui';
import UiButton from '../ui/Button.vue';
import UiField from '../ui/Field.vue';

export interface HubUser {
  id: string;
  username: string;
  role: string;
  disabled?: boolean;
  has_passkey?: boolean;
  last_seen_at?: string | null;
  /** The role comes from the identity provider's groups (Settings → Security → Sign-in): change it there. */
  role_from_groups?: boolean;
}

const ui = useUi();
const session = useSession();
const uid = useId();
const now = Date.now();

const users = ref<HubUser[]>([]);
const status = ref<'loading' | 'ready' | 'error'>('loading');
const loadError = ref('');
const busy = ref('');
const rowError = reactive<Record<string, string>>({});

const me = computed(() => session.state.user);
const myRole = computed(() => asRole(me.value?.role));
const assignable = computed(() => assignableRoles(me.value?.role));

/** Can the viewer change this user at all? */
function manageable(u: HubUser): boolean {
  if (u.id === me.value?.id) return false;
  if (u.role === 'owner') return false;
  return myRole.value === 'owner' || myRole.value === 'admin';
}
const canTransfer = (u: HubUser) => myRole.value === 'owner' && u.id !== me.value?.id && !u.disabled;

const sorted = computed(() =>
  [...users.value].sort((a, b) => Number(!!a.disabled) - Number(!!b.disabled) || (a.role === 'owner' ? -1 : b.role === 'owner' ? 1 : a.username.localeCompare(b.username))),
);

async function load(): Promise<void> {
  try {
    const res = await api<HubUser[] | { users?: HubUser[] }>('/api/users');
    users.value = Array.isArray(res) ? res : (res?.users ?? []);
    status.value = 'ready';
  } catch (err) {
    status.value = 'error';
    loadError.value = hubWords(err, 'Users couldn’t be loaded.');
  }
}

function merge(id: string, next: Partial<HubUser> | undefined, fallback: Partial<HubUser>): void {
  users.value = users.value.map((u) => (u.id === id ? { ...u, ...fallback, ...(next ?? {}) } : u));
}

async function change(u: HubUser, body: { role?: string; disabled?: boolean }, done: string): Promise<void> {
  if (busy.value === u.id) return;
  busy.value = u.id;
  rowError[u.id] = '';
  try {
    const res = await ui.withStepUp(`Change ${u.username}`, () => api<HubUser | undefined>(`/api/users/${encodeURIComponent(u.id)}`, { method: 'PUT', body }));
    merge(u.id, res, body);
    ui.toast(done);
  } catch (err) {
    if (!(err instanceof StepUpCancelled)) rowError[u.id] = hubWords(err, 'That wasn’t changed.');
  } finally {
    // Either way the select shows what the hub has.
    if (body.role !== undefined) delete roleDraft[u.id];
    busy.value = '';
  }
}

/** Roles picked in a row's select and not saved yet. */
const roleDraft = reactive<Record<string, string>>({});
const roleDirty = (u: HubUser) => roleDraft[u.id] !== undefined && roleDraft[u.id] !== u.role;

function pickRole(u: HubUser, e: Event): void {
  roleDraft[u.id] = (e.target as HTMLSelectElement).value;
}

function saveRole(u: HubUser): void {
  const role = roleDraft[u.id];
  if (role === undefined || role === u.role || busy.value === u.id) return;
  void change(u, { role }, `${u.username} is now ${ROLE_WORDS[asRole(role)].label.toLowerCase()}.`);
}

const toggleDisabled = (u: HubUser) =>
  change(u, { disabled: !u.disabled }, u.disabled ? `${u.username} can sign in again.` : `${u.username} can’t sign in any more. Their history stays in the audit log.`);

// ── add a user ──
const adding = ref(false);
const draft = reactive({ username: '', password: '', role: 'viewer' as Role });
const addErrors = reactive<{ username: string | undefined; password: string | undefined; form: string | undefined }>({ username: undefined, password: undefined, form: undefined });
const saving = ref(false);

async function startAdd(): Promise<void> {
  adding.value = true;
  draft.username = '';
  draft.password = '';
  draft.role = 'viewer';
  Object.assign(addErrors, { username: undefined, password: undefined, form: undefined });
  await nextTick();
  document.getElementById(`${uid}-un`)?.focus();
}

function cancelAdd(): void {
  adding.value = false;
  void focusFirst(`${uid}-add`, 'us-h');
}

async function add(): Promise<void> {
  if (saving.value) return;
  addErrors.username = USERNAME_RE.test(draft.username) ? undefined : 'Letters, digits and . _ @ -, starting with a letter or digit; at most 64.';
  addErrors.password = passwordLength(draft.password) >= MIN_PASSWORD ? undefined : `At least ${MIN_PASSWORD} characters.`;
  addErrors.form = undefined;
  if (addErrors.username || addErrors.password) return;
  saving.value = true;
  try {
    const body = { username: draft.username, password: draft.password, role: draft.role };
    const created = await ui.withStepUp(`Add ${draft.username}`, () => api<HubUser | { user?: HubUser }>('/api/users', { method: 'POST', body }));
    const u = created && 'username' in created ? created : created && 'user' in created ? created.user : undefined;
    if (u) users.value = [...users.value, u];
    else await load();
    ui.toast(`Added ${draft.username} as ${ROLE_WORDS[draft.role].label.toLowerCase()}. Give them their password in person.`);
    draft.password = '';
    adding.value = false;
    void focusFirst(`${uid}-add`, 'us-h');
  } catch (err) {
    if (!(err instanceof StepUpCancelled)) addErrors.form = hubWords(err, 'The user wasn’t added.');
  } finally {
    saving.value = false;
  }
}

// ── transfer ownership (T3) ──
const transferring = ref('');
const typed = ref('');
const transferError = ref('');

async function startTransfer(u: HubUser): Promise<void> {
  transferring.value = u.id;
  typed.value = '';
  transferError.value = '';
  await nextTick();
  document.getElementById(`${uid}-tr-${u.id}`)?.focus();
}

function cancelTransfer(u: HubUser): void {
  transferring.value = '';
  void focusFirst(`${uid}-mo-${u.id}`, 'us-h');
}

async function transfer(u: HubUser): Promise<void> {
  if (busy.value === u.id) return;
  if (typed.value.trim() !== u.username) {
    transferError.value = `Type ${u.username} exactly to confirm.`;
    return;
  }
  busy.value = u.id;
  transferError.value = '';
  try {
    await ui.withStepUp(`Hand ownership to ${u.username}`, () =>
      api(`/api/users/${encodeURIComponent(u.id)}/transfer-ownership`, { method: 'POST', body: { typed: typed.value.trim() } }),
    );
    transferring.value = '';
    ui.toast(`${u.username} is the owner now.`);
    // The hub says what we are now (an admin, unless it decided otherwise).
    try {
      const s = await api<{ user?: User }>('/api/auth/session');
      if (s?.user) session.state.user = s.user;
    } catch {
      /* the next session check catches up */
    }
    await load();
    // You're not the owner any more, so the row's controls changed: back to the heading.
    void focusFirst('us-h');
  } catch (err) {
    if (!(err instanceof StepUpCancelled)) transferError.value = hubWords(err, 'Ownership wasn’t handed over.');
  } finally {
    busy.value = '';
  }
}

onMounted(load);
</script>

<template>
  <section class="st-section" aria-labelledby="us-h">
    <header>
      <h3 id="us-h" tabindex="-1">People</h3>
      <span v-if="status === 'ready'" class="st-meta">{{ sorted.length }}</span>
      <UiButton v-if="assignable.length && !adding" size="sm" :id="`${uid}-add`" @click="startAdd">Add a user</UiButton>
    </header>
    <SettingsHelp summary="Everyone who signs in, and what their role lets them do.">
      Everyone who signs in to this hub, and what their role lets them do. There’s one owner; users are disabled rather than deleted, so the audit log keeps
      pointing at someone.
    </SettingsHelp>

    <form v-if="adding" class="st-form us-add" novalidate aria-labelledby="us-add-h" @submit.prevent="add">
      <h4 id="us-add-h" class="sr">Add a user</h4>
      <UiField :id="`${uid}-un`" label="Username" :error="addErrors.username" v-slot="{ control }">
        <input v-bind="control" v-model="draft.username" type="text" autocomplete="off" autocapitalize="off" spellcheck="false" maxlength="64" />
      </UiField>
      <UiField label="Password" :help="`At least ${MIN_PASSWORD} characters. They can add a passkey after signing in.`" :error="addErrors.password" v-slot="{ control }">
        <input v-bind="control" v-model="draft.password" type="password" autocomplete="new-password" />
      </UiField>
      <UiField label="Role" :help="ROLE_WORDS[draft.role].note" v-slot="{ control }">
        <select v-bind="control" v-model="draft.role">
          <option v-for="r in assignable" :key="r" :value="r">{{ ROLE_WORDS[r].label }}</option>
        </select>
      </UiField>
      <p v-if="addErrors.form" class="err" role="alert">{{ addErrors.form }}</p>
      <div class="st-acts">
        <UiButton variant="primary" type="submit" :aria-disabled="saving ? 'true' : undefined" :aria-busy="saving || undefined">{{ saving ? 'Adding…' : 'Add user' }}</UiButton>
        <UiButton variant="ghost" @click="cancelAdd">Cancel</UiButton>
      </div>
    </form>

    <div v-if="status === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span><span class="skel"></span></div>
    <div v-else-if="status === 'error'" class="st-note bad" role="alert"><Glyph kind="down" /><span>{{ loadError }}</span></div>
    <ul v-else class="st-list">
      <li v-for="u in sorted" :key="u.id">
        <div :class="['st-row', 'us-row', { off: u.disabled }]">
          <span class="mono-badge" aria-hidden="true">{{ u.username.slice(0, 2).toUpperCase() }}</span>
          <span>
            <span class="t">
              {{ u.username }}
              <span v-if="u.id === me?.id" class="chip">you</span>
              <span v-if="u.disabled" class="chip">disabled</span>
            </span>
            <span class="m">
              {{ ROLE_WORDS[asRole(u.role)].label }}<template v-if="u.role_from_groups"> <span class="chip" :title="`${u.username}’s role comes from their groups at the sign-in provider. Change it there.`">from groups</span></template> ·
              <template v-if="u.has_passkey">passkey</template><template v-else>no passkey</template> ·
              <template v-if="u.last_seen_at">seen <span :title="absolute(u.last_seen_at)">{{ ago(u.last_seen_at, now) }}</span></template>
              <template v-else>never signed in</template>
            </span>
            <span v-if="rowError[u.id]" class="err" role="alert">{{ rowError[u.id] }}</span>
          </span>
          <span v-if="manageable(u) || canTransfer(u)" class="end us-acts">
            <template v-if="manageable(u)">
              <template v-if="!u.role_from_groups">
                <label :for="`${uid}-r-${u.id}`" class="sr">Role for {{ u.username }}</label>
                <select :id="`${uid}-r-${u.id}`" class="us-role" :value="roleDraft[u.id] ?? u.role" :disabled="u.disabled" @change="pickRole(u, $event)">
                  <option v-if="!assignable.includes(asRole(u.role))" :value="u.role" disabled>{{ ROLE_WORDS[asRole(u.role)].label }}</option>
                  <option v-for="r in assignable" :key="r" :value="r">{{ ROLE_WORDS[r].label }}</option>
                </select>
                <UiButton v-if="!u.disabled" size="sm" :aria-label="`Save role for ${u.username}`" :aria-disabled="!roleDirty(u) || busy === u.id ? 'true' : undefined" :aria-busy="(busy === u.id && roleDirty(u)) || undefined" @click="saveRole(u)">
                  Save
                </UiButton>
              </template>
              <UiButton variant="ghost" size="sm" :aria-disabled="busy === u.id ? 'true' : undefined" @click="toggleDisabled(u)">{{ u.disabled ? 'Enable' : 'Disable' }}</UiButton>
            </template>
            <UiButton v-if="canTransfer(u) && transferring !== u.id" variant="ghost" size="sm" :id="`${uid}-mo-${u.id}`" @click="startTransfer(u)">Make owner…</UiButton>
          </span>
        </div>
        <form v-if="transferring === u.id" class="st-confirm us-transfer" :aria-labelledby="`${uid}-tr-h-${u.id}`" novalidate @submit.prevent="transfer(u)">
          <span :id="`${uid}-tr-h-${u.id}`">
            Hand ownership to <b>{{ u.username }}</b>? They get everything, including users and security; you become an admin. Only they can hand it back.
            <b>This can’t be undone from your side.</b>
          </span>
          <UiField :id="`${uid}-tr-${u.id}`" class="us-type" :error="transferError" alert>
            <template #label>Type <b class="st-mono">{{ u.username }}</b> to confirm</template>
            <template #default="{ control }">
              <input v-bind="control" v-model="typed" type="text" autocomplete="off" autocapitalize="off" spellcheck="false" />
            </template>
          </UiField>
          <UiButton variant="danger-solid" type="submit" :disabled="typed.trim() !== u.username" :aria-disabled="busy === u.id ? 'true' : undefined" :aria-busy="busy === u.id || undefined">Make {{ u.username }} owner<span class="tier">T3</span></UiButton>
          <UiButton @click="cancelTransfer(u)">Cancel</UiButton>
        </form>
      </li>
    </ul>
  </section>
  <BannedAddresses />
</template>

<style>
.us-row.off .t,
.us-row.off .m {
  color: var(--text-3);
}
.us-acts {
  flex-wrap: wrap;
  justify-content: flex-end;
}
.us-role {
  min-height: 32px;
  padding: 4px 8px;
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-md);
  background: var(--surface);
  font: var(--weight-medium) var(--type-small) / 1.2 var(--font-ui);
  color: var(--text);
}
.us-add {
  padding: var(--space-4);
  border: var(--hairline) solid var(--line);
  border-radius: var(--radius-lg);
}
.us-transfer {
  margin: 0 var(--space-2) var(--space-3);
}
.us-type {
  flex: 1 1 100%;
}
</style>
