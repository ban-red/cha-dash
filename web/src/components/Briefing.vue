<script setup lang="ts">
/**
 * The Briefing: a status sentence (A) or a headline with a deck (Paper),
 * the clock, and the Inbox. Phrases that name an incident open it in the Inbox.
 *
 * The copy is assembled as parts (text, bold, or a phrase bound to an
 * incident) so spacing and punctuation never depend on template whitespace.
 */
import { computed } from 'vue';
import Glyph from './marks/Glyph.vue';
import ThemeMenu from './ThemeMenu.vue';
import { fmtPct, hhmm, longDate, numberWord, plural, shortDate } from '../lib/format';
import { currentTheme, THEMES } from '../lib/theme';
import { inSentence, joinAnd, peoplePhrase, summarize } from '../model/briefing';
import { groupInbox, nbHyphen } from '../model/inbox';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';

interface Part {
  text: string;
  bold?: boolean;
  /** A phrase that opens the Inbox on this incident ('' opens it on the repairs). */
  open?: string;
}

const emit = defineEmits<{ signOut: [] }>();
const live = useLive();
const ui = useUi();

const s = computed(() => summarize(live.graph.value, live.metrics.value, live.inbox.value, live.now.value));
const headline = computed(() => THEMES.find((t) => t.id === currentTheme.value)?.briefing === 'headline');
/** What the Inbox lists as open: needs you, not already being fixed. */
const inboxCount = computed(() => groupInbox(live.inbox.value, live.fixes).open.length);
const mac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
const clock = computed(() => hhmm(live.now.value));
const eyebrow = computed(() => {
  if (live.stale.value && live.offlineAt.value) return ['Last known', `as of ${hhmm(live.offlineAt.value)}`];
  if (!s.value.ready) return ['Connecting', shortDate(live.now.value).toUpperCase()];
  return ['Status', shortDate(live.now.value).toUpperCase()];
});
const cap = (t: string) => (t ? t[0]!.toUpperCase() + t.slice(1) : t);
const glyph = computed(() => (!s.value.ready ? 'prog' : s.value.worst === 'down' ? 'down' : s.value.worst === 'attention' ? 'attn' : 'ok'));
const glyphLabel = computed(() => (!s.value.ready ? 'connecting' : s.value.worst === 'ok' ? 'all healthy' : 'needs attention'));

/** Sentence style, line one: "▲ 4 things need you · Backup of … · PBS …" or "All quiet · …". */
const line1 = computed<Part[]>(() => {
  const v = s.value;
  if (!v.ready) return [{ text: 'Reading your home…' }];
  if (v.problems.length) {
    const parts: Part[] = [{ text: `${plural(v.problems.length, 'thing')} need${v.problems.length === 1 ? 's' : ''} you`, bold: true }];
    for (const p of v.problems) parts.push({ text: ' · ' }, { text: nbHyphen(p.title), open: p.id });
    return parts;
  }
  const quiet = [
    `All quiet · ${v.servicesHealthy} services healthy`,
    v.lastBackup ? `backups done ${hhmm(v.lastBackup.at)}` : '',
    v.arrayPct !== null ? `array ${fmtPct(v.arrayPct)}%` : '',
  ];
  return [{ text: quiet.filter(Boolean).join(' · ') }];
});

/** Sentence style, line two: context, then the repairs as a phrase. */
const line2 = computed<Part[]>(() => {
  const v = s.value;
  if (!v.ready) return [{ text: 'Cha Dash is connecting to the hub. Values appear as soon as it answers.' }];
  const ctx = [
    v.problems.length ? `${v.servicesHealthy} of ${v.servicesTotal} services healthy` : '',
    v.cameras ? plural(v.cameras, 'camera') : '',
    peoplePhrase(v),
    v.next ? `next: ${v.next.title} ${hhmm(v.next.at)}` : '',
  ]
    .filter(Boolean)
    .join(' · ');
  const parts: Part[] = ctx ? [{ text: ctx }] : [];
  if (v.repairs.length) {
    if (ctx) parts.push({ text: ' · ' });
    parts.push({ text: `${plural(v.repairs.length, 'integration')} to reconnect`, open: v.repairs[0]?.id ?? '' });
  }
  return parts;
});

/** Headline style: "Four things need you: a backup of …, PBS … and qBittorrent …." */
const headlineParts = computed<Part[]>(() => {
  const v = s.value;
  if (!v.ready) return [{ text: 'Reading your home…' }];
  if (!v.problems.length) return [{ text: 'All quiet at home.' }];
  const n = v.problems.length;
  const parts: Part[] = [{ text: `${cap(numberWord(n))} ${n === 1 ? 'thing needs' : 'things need'} you: ` }];
  v.problems.forEach((p, i) => {
    if (i > 0) parts.push({ text: i === n - 1 ? ' and ' : ', ' });
    parts.push({ text: nbHyphen(inSentence(p.title)), open: p.id });
  });
  parts.push({ text: '.' });
  return parts;
});

const deck = computed(() => {
  const v = s.value;
  if (!v.ready) return '';
  const fine = joinAnd(
    [
      v.servicesTotal ? `${v.servicesHealthy} of ${v.servicesTotal} services healthy` : '',
      v.cameras ? `${numberWord(v.cameras)} cameras watching` : '',
      peoplePhrase(v, true),
    ].filter(Boolean),
  );
  const out: string[] = [];
  if (v.problems.length) out.push(`Everything else is fine${fine ? `: ${fine}` : ''}.`);
  else {
    if (fine) out.push(`${cap(fine)}.`);
    if (v.lastBackup) out.push(`Last night’s backups finished at ${hhmm(v.lastBackup.at)}.`);
  }
  if (v.next) out.push(`Next: ${v.next.title} at ${hhmm(v.next.at)}.`);
  const r = v.repairs.length;
  if (r) out.push(`${r === 1 ? 'One integration needs' : `${cap(numberWord(r))} integrations need`} reconnecting in the Inbox.`);
  return out.join(' ');
});

const open = (id: string) => ui.openInbox(id);
</script>

<template>
  <section class="brief" aria-label="Briefing">
    <div class="masthead">
      <div class="wordmark">Cha <i>Dash</i></div>
      <div class="mast-meta">
        <span>{{ longDate(live.now.value) }}</span>
        <span class="n">{{ clock }}</span>
        <button type="button" class="pill" aria-haspopup="dialog" @click="ui.openInbox()">
          Inbox <span :class="['badge', { zero: !inboxCount }]">{{ inboxCount }}</span>
        </button>
      </div>
    </div>

    <div>
      <div class="eyebrow">
        <span>{{ eyebrow[0] }}</span>
        <span>{{ eyebrow[1] }}</span>
      </div>
      <h1 class="sentence">
        <Glyph :kind="glyph" :label="glyphLabel" />
        <template v-for="(p, k) in headline ? headlineParts : line1" :key="k">
          <button v-if="p.open !== undefined" type="button" class="phrase" @click="open(p.open)">{{ p.text }}</button>
          <b v-else-if="p.bold">{{ p.text }}</b>
          <template v-else>{{ p.text }}</template>
        </template>
      </h1>
      <p v-if="headline" class="sentence-2">{{ deck }}</p>
      <p v-else class="sentence-2">
        <Glyph v-if="s.ready && s.problems.length" kind="ok" />
        <template v-for="(p, k) in line2" :key="k">
          <button v-if="p.open !== undefined" type="button" class="phrase" @click="open(p.open)">{{ p.text }}</button>
          <template v-else>{{ p.text }}</template>
        </template>
      </p>
    </div>

    <div class="brief-side">
      <div class="clock" aria-label="Time at home">{{ clock }}</div>
      <div class="brief-tools">
        <button type="button" class="pill" aria-haspopup="dialog" :aria-label="`Inbox: ${inboxCount} need you`" @click="ui.openInbox()">
          Inbox <span :class="['badge', { zero: !inboxCount }]">{{ inboxCount }}</span>
        </button>
        <button type="button" class="tool search-tool" aria-haspopup="dialog" :aria-keyshortcuts="mac ? 'Meta+K' : 'Control+K'" aria-label="Search and commands" :title="`Search and commands (${mac ? '⌘K' : 'Ctrl K'})`" @click="ui.openPalette()">
          <svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="7" cy="7" r="4.6" /><path d="m10.4 10.4 3.4 3.4" /></svg>
        </button>
        <ThemeMenu @sign-out="emit('signOut')" />
      </div>
    </div>
  </section>
</template>
