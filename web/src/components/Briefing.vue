<script setup lang="ts">
/**
 * The Briefing: a status sentence (A) or a headline with a deck (Paper),
 * the clock, and the Inbox. Phrases that name an incident open it in the Inbox.
 * Under them, the Pulse strip: the last day's health, which scrubs the board.
 *
 * The copy is assembled as parts (text, bold, or a phrase bound to an
 * incident) so spacing and punctuation never depend on template whitespace.
 */
import { computed } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import BriefTools from './BriefTools.vue';
import PulseStrip from './PulseStrip.vue';
import { fmtPct, hhmm, longDate, numberWord, plural, shortDate } from '../lib/format';
import { currentTheme, THEMES } from '../lib/theme';
import { inSentence, joinAnd, peoplePhrase, summarize } from '../model/briefing';
import { nbHyphen } from '../model/inbox';
import { useLive } from '../stores/live';
import { isMuted } from '../stores/muted';
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

const s = computed(() => summarize(live.graph.value, live.metrics.value, live.inbox.value, live.now.value, isMuted));
const headline = computed(() => THEMES.find((t) => t.id === currentTheme.value)?.briefing === 'headline');
const clock = computed(() => hhmm(live.now.value));
const eyebrow = computed(() => {
  if (live.stale.value && live.offlineAt.value) return ['Last known', `as of ${hhmm(live.offlineAt.value)}`];
  if (!s.value.ready) return ['Connecting', shortDate(live.now.value).toUpperCase()];
  return ['Status', shortDate(live.now.value).toUpperCase()];
});
const cap = (t: string) => (t ? t[0]!.toUpperCase() + t.slice(1) : t);
const glyph = computed(() => (!s.value.ready ? 'prog' : s.value.worst === 'down' ? 'down' : s.value.worst === 'attention' ? 'attn' : 'ok'));
const glyphLabel = computed(() => (!s.value.ready ? 'connecting' : s.value.worst === 'ok' ? 'all healthy' : 'needs attention'));

/** How many problems the Briefing names; the rest are "N more", which opens the Inbox. */
const NAMED = { sentence: 3, headline: 2 };

/** Sentence style, line one: "▲ 4 things need you · Backup of … · PBS …" or "All quiet · …". */
const line1 = computed<Part[]>(() => {
  const v = s.value;
  if (!v.ready) return [{ text: 'Reading your home…' }];
  if (v.problems.length) {
    const parts: Part[] = [{ text: `${plural(v.problems.length, 'thing')} need${v.problems.length === 1 ? 's' : ''} you`, bold: true }];
    const named = v.problems.slice(0, NAMED.sentence);
    for (const p of named) parts.push({ text: ' · ' }, { text: nbHyphen(p.title), open: p.id });
    const rest = v.problems.length - named.length;
    if (rest) parts.push({ text: ' · ' }, { text: `${rest} more`, open: '' });
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
  // Name the first two; a headline that lists seven things is a paragraph.
  const named = v.problems.slice(0, NAMED.headline);
  const rest = n - named.length;
  named.forEach((p, i) => {
    if (i > 0) parts.push({ text: rest || i < named.length - 1 ? ', ' : ' and ' });
    parts.push({ text: nbHyphen(inSentence(p.title)), open: p.id });
  });
  if (rest) parts.push({ text: ' and ' }, { text: `${numberWord(rest)} more`, open: '' });
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

const open = (id: string) => (id ? ui.openInbox(id) : ui.openInbox());
/** Phrases are spans, not buttons: a button is laid out as a box, so punctuation after it wraps to the next line. */
function key(e: KeyboardEvent, id: string) {
  if (e.key === 'Enter' || e.key === ' ') {
    e.preventDefault();
    open(id);
  }
}
</script>

<template>
  <section class="brief" aria-label="Briefing">
    <div class="masthead">
      <div class="wordmark">Cha <i>Dash</i><span v-if="live.demo.value" class="demo-tag">Demo</span></div>
      <div class="mast-meta">
        <span>{{ longDate(live.now.value) }}</span>
        <span class="n">{{ clock }}</span>
        <BriefTools v-if="headline" @sign-out="emit('signOut')" />
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
          <span v-if="p.open !== undefined && ui.can.value.inbox" role="button" tabindex="0" class="phrase" @click="open(p.open)" @keydown="key($event, p.open)">{{ p.text }}</span>
          <b v-else-if="p.bold">{{ p.text }}</b>
          <template v-else>{{ p.text }}</template>
        </template>
      </h1>
      <p v-if="headline" class="sentence-2">{{ deck }}</p>
      <p v-else class="sentence-2">
        <Glyph v-if="s.ready && s.problems.length" kind="ok" />
        <template v-for="(p, k) in line2" :key="k">
          <span v-if="p.open !== undefined && ui.can.value.inbox" role="button" tabindex="0" class="phrase" @click="open(p.open)" @keydown="key($event, p.open)">{{ p.text }}</span>
          <template v-else>{{ p.text }}</template>
        </template>
      </p>
      <PulseStrip v-if="s.ready && !live.demo.value" />
    </div>

    <div class="brief-side">
      <span class="sr">{{ live.demo.value ? 'Simulated time in the Demo Home:' : 'Time at home:' }}</span>
      <div class="clock">{{ clock }}</div>
      <BriefTools v-if="!headline" @sign-out="emit('signOut')" />
    </div>
  </section>
</template>
