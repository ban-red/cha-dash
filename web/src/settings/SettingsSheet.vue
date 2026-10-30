<script setup lang="ts">
/**
 * Settings: a sheet over the front page, not a separate admin app
 * (02 · Experience → Surfaces). The route picks the pane:
 * /settings/integrations, /settings/stacks, /settings/nodes, /settings/import, /settings/backup, /settings/history,
 * /settings/users, /settings/audit, /settings/notifications, /settings/tokens,
 * /settings/security. The board, the
 * socket and the Inbox stay live underneath; Esc or the scrim goes back.
 *
 * Only owners and admins open Settings; the panes listed are the ones the
 * viewer's role can use (model/roles.ts). The hub enforces it either way.
 *
 * Its own chunk, loaded the first time Settings opens. Data sits on a solid
 * surface; glass stays on the page's chrome.
 */
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, onMounted, ref } from 'vue';
import IntegrationsPane from './IntegrationsPane.vue';
import SecurityPane from './SecurityPane.vue';
import { back, navigate, settingsPath, view, type SettingsPane } from '../lib/router';
import { layerAbove, trapTab } from '../lib/trap';
import { useUi } from '../stores/ui';
import UiCloseButton from '../ui/CloseButton.vue';

// Rarely opened: each its own chunk, loaded with the pane.
const StacksPane = defineAsyncComponent(() => import('./StacksPane.vue'));
const NodesPane = defineAsyncComponent(() => import('./NodesPane.vue'));
const BackupPane = defineAsyncComponent(() => import('./BackupPane.vue'));
const HistoryPane = defineAsyncComponent(() => import('./HistoryPane.vue'));
const BringInPane = defineAsyncComponent(() => import('../import/BringInPane.vue'));
const UsersPane = defineAsyncComponent(() => import('./UsersPane.vue'));
const AuditPane = defineAsyncComponent(() => import('./AuditPane.vue'));
const NotificationsPane = defineAsyncComponent(() => import('./NotificationsPane.vue'));
const TokensPane = defineAsyncComponent(() => import('./TokensPane.vue'));

const ui = useUi();

const panel = ref<HTMLElement | null>(null);
let lastFocus: Element | null = null;

const ALL_PANES: { id: SettingsPane; label: string; note: string }[] = [
  { id: 'integrations', label: 'Integrations', note: 'what Cha Dash reads' },
  { id: 'stacks', label: 'Stacks', note: 'compose, deploy, roll back' },
  { id: 'nodes', label: 'Nodes', note: 'hosts Cha Dash acts on' },
  { id: 'import', label: 'Bring in', note: 'import and discover' },
  { id: 'backup', label: 'Backup', note: 'export and restore' },
  { id: 'history', label: 'History', note: 'what Cha Dash keeps' },
  { id: 'users', label: 'Users', note: 'people and roles' },
  { id: 'audit', label: 'Audit', note: 'who did what' },
  { id: 'notifications', label: 'Notifications', note: 'push, ntfy, webhooks' },
  { id: 'tokens', label: 'API tokens', note: 'scripts and automations' },
  { id: 'security', label: 'Security', note: 'level, step-up, passkeys' },
];
const PANES = computed(() =>
  ALL_PANES.filter((p) => ui.can.value.panes.includes(p.id)).map((p) =>
    p.id === 'security' && !ui.can.value.securitySettings ? { ...p, note: 'passkeys' } : p.id === 'notifications' && !ui.can.value.users ? { ...p, note: 'this device' } : p,
  ),
);
/** The nav in three groups, so eleven panes read as three decisions. A group with no pane for this role is left out. */
const GROUPS: { label: string; ids: SettingsPane[] }[] = [
  { label: 'Connect', ids: ['integrations', 'stacks', 'nodes', 'import'] },
  { label: 'Keep', ids: ['backup', 'history'] },
  { label: 'People & access', ids: ['users', 'tokens', 'security', 'notifications', 'audit'] },
];
const NAV = computed(() =>
  GROUPS.map((g) => ({ label: g.label, panes: g.ids.flatMap((id) => PANES.value.filter((p) => p.id === id)) })).filter((g) => g.panes.length),
);
const note = computed(() => PANES.value.find((p) => p.id === pane.value)?.note ?? '');
/** The route's pane, or the first one this role has. */
const pane = computed<SettingsPane>(() => {
  const want = view.value.name === 'settings' ? view.value.pane : 'integrations';
  return ui.can.value.panes.includes(want) ? want : (ui.can.value.panes[0] ?? 'integrations');
});
const TITLES: Record<SettingsPane, string> = {
  integrations: 'Integrations',
  stacks: 'Stacks',
  nodes: 'Nodes',
  import: 'Bring in',
  backup: 'Backup',
  history: 'History',
  users: 'Users',
  audit: 'Audit',
  notifications: 'Notifications',
  tokens: 'API tokens',
  security: 'Security',
};
const title = computed(() => TITLES[pane.value]);

function close(): void {
  back('/');
}

function onKey(e: KeyboardEvent): void {
  if (e.defaultPrevented || (e.key !== 'Escape' && e.key !== 'Tab')) return;
  // A layer above Settings (the command bar, the Inbox, a confirm, the
  // step-up or any other modal <dialog>) handles its own keys.
  if (layerAbove(ui.state) || document.querySelector('.layer-palette, .layer:not(.inert) .inbox')) return;
  if (e.key === 'Tab') {
    // aria-modal: Tab stays in the sheet, not on the task strip or toasts behind it.
    trapTab(e, panel.value);
    return;
  }
  e.preventDefault();
  close();
}

onMounted(async () => {
  lastFocus = document.activeElement;
  document.addEventListener('keydown', onKey);
  await nextTick();
  // A pane may focus a field itself (a repair's Reconnect); otherwise the sheet takes focus.
  if (!panel.value?.contains(document.activeElement)) panel.value?.focus({ preventScroll: true });
});
onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKey);
  if (lastFocus instanceof HTMLElement && lastFocus.isConnected) lastFocus.focus({ preventScroll: true });
});
</script>

<template>
  <Teleport to="body">
    <div class="layer layer-settings">
      <div class="scrim st-scrim" @click="close"></div>
      <section ref="panel" class="settings" role="dialog" aria-modal="true" aria-labelledby="settings-h" tabindex="-1">
        <header class="st-head">
          <div class="st-title">
            <span class="caps">Settings</span>
            <h2 id="settings-h">{{ title }}</h2>
            <p v-if="note" class="st-sub">{{ note }}</p>
          </div>
          <UiCloseButton label="Close settings" @click="close" />
        </header>
        <div class="st-body">
          <nav class="st-nav" aria-label="Settings sections">
            <div v-for="g in NAV" :key="g.label" class="st-group" role="group" :aria-label="g.label">
              <span class="st-group-h" aria-hidden="true">{{ g.label }}</span>
              <a
                v-for="p in g.panes"
                :key="p.id"
                :href="settingsPath(p.id)"
                :aria-current="pane === p.id ? 'page' : undefined"
                @click.prevent="navigate(settingsPath(p.id), { replace: true })"
              >
                <span>{{ p.label }}</span><small>{{ p.note }}</small>
              </a>
            </div>
          </nav>
          <div class="st-pane">
            <IntegrationsPane v-if="pane === 'integrations'" />
            <StacksPane v-else-if="pane === 'stacks'" />
            <NodesPane v-else-if="pane === 'nodes'" />
            <BringInPane v-else-if="pane === 'import'" />
            <BackupPane v-else-if="pane === 'backup'" />
            <HistoryPane v-else-if="pane === 'history'" />
            <UsersPane v-else-if="pane === 'users'" />
            <AuditPane v-else-if="pane === 'audit'" />
            <NotificationsPane v-else-if="pane === 'notifications'" />
            <TokensPane v-else-if="pane === 'tokens'" />
            <SecurityPane v-else />
          </div>
        </div>
      </section>
    </div>
  </Teleport>
</template>

<style>
/* ───────────── Settings sheet ───────────── */
.layer-settings {
  z-index: 35;
}
.st-scrim {
  animation: st-fade var(--motion-sheet-duration) var(--motion-sheet-easing);
}
.settings {
  position: absolute;
  top: 0;
  right: 0;
  bottom: 0;
  display: flex;
  flex-direction: column;
  width: min(960px, 100%);
  background: var(--bg);
  background-image: var(--backdrop);
  border-left: var(--hairline) solid var(--line-2);
  box-shadow: var(--shadow-overlay);
  outline: none;
  animation: st-in var(--motion-sheet-duration) var(--motion-sheet-easing);
}
@keyframes st-in {
  from {
    transform: translateX(32px);
    opacity: 0;
  }
}
@keyframes st-fade {
  from {
    opacity: 0;
  }
}
@media (prefers-reduced-motion: reduce) {
  html:not([data-motion='full']) .settings {
    animation-name: st-fade;
  }
}
.st-head {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: var(--space-4);
  padding: calc(var(--space-6) + env(safe-area-inset-top)) var(--space-6) var(--space-4);
  border-bottom: var(--hairline) solid var(--line);
}
.st-title {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
.st-head h2 {
  font: var(--weight-medium) var(--type-title-2) / 1.1 var(--font-ui);
  letter-spacing: -0.012em;
}
.st-sub {
  font-size: var(--type-small);
  color: var(--text-3);
}
:root[data-briefing='headline'] .st-head h2 {
  font: var(--weight-regular) 30px/1.05 var(--font-head);
}
.st-body {
  flex: 1;
  min-height: 0;
  display: grid;
  grid-template-columns: 184px minmax(0, 1fr);
}
.st-nav {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
  padding: var(--space-4) var(--space-3);
  border-right: var(--hairline) solid var(--line);
  overflow-y: auto;
}
.st-group {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.st-group-h {
  padding: 0 10px 4px;
  font: var(--weight-medium) var(--type-micro) / 1 var(--font-num);
  letter-spacing: var(--tracking-caps);
  text-transform: uppercase;
  color: var(--text-3);
}
.st-nav a {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 8px 10px;
  border-radius: var(--radius-md);
  text-decoration: none;
  font: var(--weight-medium) var(--type-body) / 1.2 var(--font-ui);
  color: var(--text-2);
}
.st-nav a small {
  font: var(--weight-regular) var(--type-caption) / 1.2 var(--font-num);
  color: var(--text-3);
}
.st-nav a:hover {
  background: var(--surface-hover);
  color: var(--text);
}
.st-nav a:hover small {
  color: var(--text-2); /* text-3 is too faint on a hover row (WCAG AA) */
}
.st-nav a[aria-current='page'] {
  background: var(--surface);
  box-shadow: 0 0 0 var(--hairline) var(--line-2);
  color: var(--text);
}
.st-pane {
  min-width: 0;
  overflow: auto;
  overscroll-behavior: contain;
  /* A field Tab reaches scrolls clear of a form's sticky Save bar (WCAG 2.4.11). */
  scroll-padding-bottom: 72px;
  padding: var(--space-6) var(--space-6) calc(var(--space-12) + env(safe-area-inset-bottom));
}
@media (max-width: 719px) {
  .st-head {
    padding: calc(var(--space-4) + env(safe-area-inset-top)) 16px var(--space-3);
  }
  .st-body {
    grid-template-columns: minmax(0, 1fr);
    grid-template-rows: auto minmax(0, 1fr);
  }
  .st-nav {
    flex-direction: row;
    gap: var(--space-2);
    overflow-x: auto;
    overflow-y: hidden;
    padding: var(--space-2) 12px;
    border-right: 0;
    border-bottom: var(--hairline) solid var(--line);
  }
  .st-group {
    flex-direction: row;
    gap: 2px;
  }
  .st-group-h,
  .st-sub {
    display: none;
  }
  .st-nav a {
    flex: none;
    min-height: var(--target-min);
    justify-content: center;
  }
  .st-nav a small {
    display: none;
  }
  .st-pane {
    padding: var(--space-5) 16px calc(var(--space-12) + env(safe-area-inset-bottom));
  }
}

/* ───────────── Shared pieces of the panes ───────────── */
.st-section {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  margin: 0 0 var(--space-8);
}
.st-section > header {
  display: flex;
  align-items: center;
  gap: var(--space-3);
}
.st-section > header h3 {
  font: var(--weight-medium) var(--type-micro) / 1 var(--font-num);
  letter-spacing: var(--tracking-caps);
  text-transform: uppercase;
  color: var(--text-3);
}
.st-section > header::after {
  content: '';
  order: 1;
  flex: 1;
  height: var(--hairline);
  background: var(--line);
}
.st-section > header > .btn,
.st-section > header > .st-meta {
  order: 2;
}
.st-meta {
  font: var(--weight-regular) var(--type-caption) / 1.2 var(--font-num);
  color: var(--text-3);
}
.st-lede {
  max-width: 62ch;
  font-size: var(--type-small);
  line-height: var(--leading-relaxed);
  color: var(--text-2);
  text-wrap: pretty;
}
.st-lede code,
.st-mono {
  font-family: var(--font-num);
  font-size: 0.95em;
  color: var(--text-2);
}
.st-crumb {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  align-self: flex-start;
  padding: 0;
  border: 0;
  background: none;
  font: var(--weight-medium) var(--type-small) / 1 var(--font-ui);
  color: var(--text-3);
  text-decoration: none;
}
.st-crumb:hover {
  color: var(--text);
}
.st-list {
  list-style: none;
  margin: 0;
  padding: 0;
  border-top: var(--hairline) solid var(--line);
}
.st-list > li {
  border-bottom: var(--hairline) solid var(--line);
}
.st-row {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  gap: var(--space-3);
  align-items: center;
  padding: var(--space-3) var(--space-2);
  color: inherit;
  text-decoration: none;
  border-radius: var(--radius-sm);
  transition: background-color var(--motion-press-duration) var(--motion-press-easing);
}
a.st-row:hover {
  background: color-mix(in oklch, var(--text) 4%, transparent);
}
.st-row .t {
  font: var(--weight-medium) var(--type-body) / 1.3 var(--font-ui);
  color: var(--text);
}
.st-row .m {
  margin-top: 2px;
  font-size: var(--type-small);
  line-height: 1.4;
  color: var(--text-3);
  overflow: hidden;
  text-overflow: ellipsis;
}
.st-row .end {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font: var(--weight-medium) var(--type-caption) / 1.2 var(--font-num);
  color: var(--text-3);
  white-space: nowrap;
}
.mono-badge {
  display: inline-grid;
  place-items: center;
  width: 30px;
  height: 30px;
  flex: none;
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-sm);
  font: 600 11px/1 var(--font-num);
  letter-spacing: 0.02em;
  color: var(--text-2);
}
.st-status {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  color: var(--text-2);
}
.st-empty {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: var(--space-3);
  padding: var(--space-6) var(--space-5);
  border: var(--hairline) dashed var(--line-2);
  border-radius: var(--radius-lg);
}
.st-empty .t {
  font: var(--weight-medium) var(--type-callout) / 1.3 var(--font-ui);
}
.st-empty .m {
  max-width: 52ch;
  font-size: var(--type-small);
  color: var(--text-3);
  line-height: var(--leading-relaxed);
}
.st-acts {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-2);
}
.st-note {
  display: flex;
  gap: var(--space-2);
  align-items: flex-start;
  padding: 10px 12px;
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-md);
  font-size: var(--type-small);
  line-height: 1.45;
  color: var(--text-2);
}
.st-note .g {
  margin-top: 3px;
}
.st-note.bad {
  border-color: color-mix(in oklch, var(--down) 55%, var(--line));
  background: var(--down-bg);
  color: var(--text);
}
.st-note.attn {
  border-color: color-mix(in oklch, var(--attention) 55%, var(--line));
  background: var(--attention-bg);
  color: var(--text);
}
.st-note.hatched {
  border-style: dashed;
  background: repeating-linear-gradient(135deg, var(--hatch) 0 1.5px, transparent 1.5px 7px);
}
.st-skel {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  padding: var(--space-3) 0;
}
.st-skel .skel:nth-child(2) {
  width: 70%;
}
.st-skel .skel:nth-child(3) {
  width: 45%;
}
.st-confirm {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-3);
  padding: 10px 12px;
  border: var(--hairline) solid color-mix(in oklch, var(--down) 50%, var(--line-2));
  border-radius: var(--radius-md);
  font-size: var(--type-small);
  color: var(--text);
}
.st-confirm > span {
  flex: 1 1 18rem;
}

/* Form controls beyond the doors' .field input */
.st-form {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  max-width: 620px;
}
.field.st-focus {
  border-radius: var(--radius-md);
  animation: st-focus 1.8s var(--motion-fade-easing);
}
@keyframes st-focus {
  0%,
  50% {
    box-shadow: 0 0 0 6px color-mix(in oklch, var(--attention) 22%, transparent);
  }
  100% {
    box-shadow: 0 0 0 6px transparent;
  }
}
.secret-row {
  display: flex;
  gap: var(--space-2);
}
.secret-row input {
  flex: 1;
  min-width: 0;
}
.secret-row .btn {
  min-height: var(--control-h, var(--target-min));
}
.set-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 2px 6px;
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-full);
  font: var(--weight-medium) 10.5px/1.2 var(--font-num);
  color: var(--text-2);
}
/* A held secret marked for removal: still there until Save. */
.set-chip.off {
  border-style: dashed;
  color: var(--text);
}
.st-fieldset {
  /* Two columns: number fields sit side by side, the rest span both. */
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px var(--space-3);
  min-width: 0;
  margin: 0;
  padding: 10px var(--space-3) var(--space-3);
  border: var(--hairline) solid var(--line);
  border-radius: var(--radius-md);
}
.st-fieldset > * {
  grid-column: 1 / -1;
}
.st-fieldset > .field.half {
  grid-column: auto;
}
@media (max-width: 420px) {
  .st-fieldset > .field.half {
    grid-column: 1 / -1;
  }
}
.st-fieldset > legend {
  padding: 0 6px;
  margin-left: -6px;
  font: var(--weight-medium) var(--type-micro) / 1 var(--font-num);
  letter-spacing: var(--tracking-caps);
  text-transform: uppercase;
  color: var(--text-3);
}
.st-array {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.st-array-item {
  position: relative;
}
.item-acts {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
}
.item-test {
  margin: 0;
}
.choice-row {
  display: flex;
  flex-wrap: wrap;
  gap: 2px;
  padding: 2px;
  border-radius: calc(var(--radius-md) + 2px);
  background: color-mix(in oklch, var(--text) 7%, transparent);
  align-self: flex-start;
}
.choice-row label {
  position: relative;
  display: inline-flex;
  align-items: center;
  min-height: 28px;
  padding: 5px 11px;
  border-radius: var(--radius-md);
  font: var(--weight-medium) var(--type-small) / 1 var(--font-ui);
  color: var(--text-2);
  cursor: pointer;
}
.choice-row input {
  position: absolute;
  opacity: 0;
  pointer-events: none;
}
.choice-row label:has(input:checked) {
  background: var(--surface);
  color: var(--text);
  box-shadow:
    0 0 0 var(--hairline) var(--line-2),
    0 1px 2px color-mix(in oklch, var(--bg) 60%, transparent);
}
.choice-row label:has(input:focus-visible) {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.check-row {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-size: var(--type-body);
  color: var(--text);
}
.check-row input {
  width: 18px;
  height: 18px;
  accent-color: var(--text);
}
.seg {
  display: flex;
  padding: 2px;
  border-radius: calc(var(--radius-md) + 2px);
  background: color-mix(in oklch, var(--text) 7%, transparent);
}
.seg button {
  flex: 1;
  padding: 7px 10px;
  border: 0;
  border-radius: var(--radius-md);
  background: transparent;
  font: var(--weight-medium) var(--type-small) / 1 var(--font-ui);
  color: var(--text-2);
  white-space: nowrap;
}
.seg button[aria-pressed='true'] {
  background: var(--surface);
  color: var(--text);
  box-shadow:
    0 0 0 var(--hairline) var(--line-2),
    0 1px 2px color-mix(in oklch, var(--bg) 60%, transparent);
}
.seg button:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}
.seg .n {
  margin-left: 6px;
  font: var(--weight-regular) var(--type-caption) / 1 var(--font-num);
  color: var(--text-3);
}
</style>
