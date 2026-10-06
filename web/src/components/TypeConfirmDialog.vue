<script setup lang="ts">
/**
 * The T3 and T4 confirm: type the name. It says exactly what will happen
 * (the plan's steps) and what will be lost or affected (its impact), says
 * plainly when it can't be undone, and only enables the button once what's
 * typed matches the plan's `confirm_text`. The hub checks it again
 * (`confirm_mismatch` is said at the field). Step-up, when the plan needs
 * it, follows this dialog.
 *
 * A native modal <dialog>: focus moves in and stays, the page behind is
 * inert, Esc cancels. Its own chunk, loaded the first time a plan asks for it.
 */
import { computed, nextTick, onMounted, ref, useId, useTemplateRef } from 'vue';
import Glyph from './marks/Glyph.vue';
import { useModal } from '../board/useModal';
import { HUB_WARNING, hitsHub, hubFirst, impactName, relationWords } from '../model/words';
import { isHub } from '../model/hub';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';

const live = useLive();
const ui = useUi();
const uid = useId();
const dlg = useTemplateRef<HTMLDialogElement>('dlg');
const input = ref<HTMLInputElement | null>(null);
useModal(dlg);

const c = computed(() => (ui.state.confirm?.kind === 'type' ? ui.state.confirm : undefined));
const plan = computed(() => c.value?.plan);
const expected = computed(() => plan.value?.confirm_text ?? '');
const typed = ref('');
const matches = computed(() => expected.value !== '' && typed.value.trim() === expected.value);
const impacts = computed(() => hubFirst(live.graph.value, plan.value?.impact ?? []).slice(0, 20));
const takesHub = computed(() => hitsHub(live.graph.value, (plan.value?.impact ?? []).map((x) => x.resource)));
const more = computed(() => (plan.value?.impact?.length ?? 0) - impacts.value.length);
const fourth = computed(() => plan.value?.tier === 'T4');

function submit(): void {
  if (!matches.value || c.value?.busy) return;
  ui.confirmed(typed.value.trim());
}

onMounted(async () => {
  await nextTick();
  input.value?.focus();
});
</script>

<template>
  <dialog ref="dlg" class="act-dlg" :aria-labelledby="`${uid}-h`" :aria-describedby="`${uid}-d`" @cancel.prevent="ui.cancelConfirm()">
    <form v-if="c && plan" class="act-dlg-body" novalidate @submit.prevent="submit">
      <header class="act-dlg-head">
        <h2 :id="`${uid}-h`">{{ plan.label }}</h2>
        <span class="chip">{{ plan.tier }}</span>
        <button type="button" class="x" aria-label="Cancel" @click="ui.cancelConfirm()">
          <svg viewBox="0 0 10 10" aria-hidden="true"><path d="m2.5 2.5 5 5m0-5-5 5" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></svg>
        </button>
      </header>

      <p v-if="c.note" class="act-note" role="note"><Glyph kind="attn" /><span>{{ c.note }}</span></p>
      <p :id="`${uid}-d`" class="act-lede">
        <template v-if="plan.undo">You can take it back afterwards: {{ plan.undo.label }}.</template>
        <template v-else><b>This can’t be undone.</b></template>
        {{ fourth ? 'It’s critical, so Cha Dash asks you to confirm it’s you every time.' : '' }}
      </p>

      <p v-if="takesHub" class="act-hub" role="alert"><Glyph kind="attn" /><span>{{ HUB_WARNING }}</span></p>

      <section v-if="plan.steps?.length" class="act-sec" :aria-labelledby="`${uid}-steps`">
        <h3 :id="`${uid}-steps`">What Cha Dash will do</h3>
        <ol class="act-steps">
          <li v-for="(s, i) in plan.steps" :key="i">{{ s.label }}</li>
        </ol>
      </section>

      <section class="act-sec" :aria-labelledby="`${uid}-lost`">
        <h3 :id="`${uid}-lost`">What will be lost or affected · {{ plan.impact?.length ?? 0 }}</h3>
        <ul v-if="impacts.length" class="act-impact">
          <li v-for="x in impacts" :key="x.resource" :class="{ 'is-self': isHub(live.graph.value, x.resource) }">
            <Glyph :kind="x.depth <= 1 || isHub(live.graph.value, x.resource) ? 'down' : 'attn'" />
            <span>{{ impactName(live.graph.value, x.resource) }}</span>
            <span class="via">{{ relationWords(x.via) }}</span>
          </li>
          <li v-if="more > 0" class="more">and {{ more }} more</li>
        </ul>
        <p v-else class="act-none">Nothing else depends on it, as far as Cha Dash can tell.</p>
      </section>

      <div class="field">
        <label :for="`${uid}-type`">Type <b class="act-name">{{ expected }}</b> to confirm</label>
        <input
          :id="`${uid}-type`"
          ref="input"
          v-model="typed"
          class="mono-input"
          type="text"
          autocomplete="off"
          autocapitalize="off"
          spellcheck="false"
          :aria-invalid="c.error ? true : undefined"
          :aria-describedby="c.error ? `${uid}-err` : undefined"
        />
        <span v-if="c.error" :id="`${uid}-err`" class="help bad" role="alert">{{ c.error }}</span>
      </div>
      <p v-if="plan.step_up !== 'none'" class="act-hint">Next, confirm it’s you with your passkey{{ fourth ? '' : ' or password' }}.</p>

      <div class="act-dlg-foot">
        <button type="button" class="btn ghost" @click="ui.cancelConfirm()">Cancel</button>
        <button type="submit" class="btn danger solid" :disabled="!matches || c.busy" :aria-busy="c.busy || undefined">
          {{ c.busy ? 'Working…' : plan.label }}<span class="tier">{{ plan.tier }}</span>
        </button>
      </div>
    </form>
  </dialog>
</template>
