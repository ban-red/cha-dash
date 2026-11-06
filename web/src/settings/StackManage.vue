<script setup lang="ts">
/**
 * One stack's place under Cha Dash: where a moved stack came from (while that
 * copy is still there, the tool that wrote it may still deploy it), Move for a
 * stack adopted in place, and Un-adopt (`stack.unadopt`, T1 with Undo), which
 * stops versioning it and leaves its files and containers as they are.
 * Above them, the whole stack's lifecycle: Stop, Start, Restart (T1), Take
 * down (T2) and Remove (T3, the name typed; deleting its volumes needs the
 * name typed here too). Each plans its action, so the dialog its tier asks
 * for shows the containers, what depends on them and the volumes.
 *
 * Shown in the stack's page (StackEditor.vue) under the file's path.
 */
import { computed, ref } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import UiButton from '../ui/Button.vue';
import UiField from '../ui/Field.vue';
import StackMove from './StackMove.vue';
import { useUi } from '../stores/ui';
import { lifecycleOffers, removeParams, targetOf, type LifecycleOffer, type StackDetail } from './stacks';

const props = defineProps<{ detail: StackDetail; /** A task is running on the stack. */ busy?: boolean }>();

const ui = useUi();
const canAct = computed(() => ui.can.value.act('T1'));
const managed = computed(() => props.detail.managed && !props.detail.protected);
const inPlace = computed(() => managed.value && props.detail.location === 'external');

const offers = computed(() => lifecycleOffers(props.detail).filter((o) => ui.can.value.act(o.tier)));
const volumes = computed(() => props.detail.volumes ?? []);
const withVolumes = ref(false);
const typedName = ref('');
const removeWith = computed(() => removeParams(props.detail.name, withVolumes.value, typedName.value));

function run(o: LifecycleOffer): void {
  const target = targetOf(props.detail.node, props.detail.name);
  if (o.action === 'stack.remove') {
    const params = removeWith.value;
    if (!params) return;
    void ui.runAction({ action: o.action, target, params, label: params.volumes ? `${o.label} and its volumes` : o.label, tier: o.tier });
    return;
  }
  void ui.runAction({ action: o.action, target, label: o.label, tier: o.tier });
}

function unadopt(): void {
  void ui.runAction({ action: 'stack.unadopt', target: targetOf(props.detail.node, props.detail.name), label: `Un-adopt ${props.detail.name}`, tier: 'T1' });
}
</script>

<template>
  <div v-if="detail.moved_from" class="st-note attn skg-copy" role="note">
    <Glyph kind="attn" />
    <span>A copy stays at <span class="st-mono">{{ detail.moved_from }}</span>; the tool that wrote it may still deploy it.</span>
  </div>
  <div v-if="offers.length" class="skl">
    <span class="st-acts skl-acts" role="group" :aria-label="`${detail.name}'s containers`">
      <UiButton
        v-for="o in offers"
        :key="o.action"
        size="sm"
        :variant="o.action === 'stack.remove' ? 'danger' : 'ghost'"
        :disabled="busy || (o.action === 'stack.remove' && !removeWith)"
        :aria-label="o.label"
        @click="run(o)"
      >
        {{ o.text }}<span class="tier">{{ o.tier }}</span>
      </UiButton>
    </span>
    <div v-if="volumes.length && offers.some((o) => o.action === 'stack.remove')" class="skl-vols">
      <UiField check :label="`Remove also deletes its volumes: ${volumes.join(', ')}`" v-slot="{ control }">
        <input v-bind="control" v-model="withVolumes" type="checkbox" :disabled="busy" />
      </UiField>
      <UiField
        v-if="withVolumes"
        :label="`Type ${detail.name} to delete the data in them`"
        :invalid="!!typedName && !removeWith"
        help="Take down and Remove keep them otherwise."
        v-slot="{ control }"
      >
        <input v-bind="control" v-model="typedName" autocomplete="off" spellcheck="false" :disabled="busy" />
      </UiField>
    </div>
  </div>
  <div v-if="managed && canAct" class="skg">
    <p class="st-meta">
      <template v-if="inPlace">Adopted where it is. Move brings the file into the stacks root, where Cha Dash keeps the rest; nothing restarts.</template>
      <template v-else>Un-adopting stops versioning {{ detail.name }}. Its files and containers stay as they are.</template>
    </p>
    <span class="st-acts skg-acts">
      <StackMove v-if="inPlace" :node="detail.node" :name="detail.name" :from-path="`${detail.path}/${detail.file}`" />
      <UiButton size="sm" variant="ghost" :disabled="busy" :aria-label="`Un-adopt ${detail.name}: stop versioning it, keep its files and containers`" @click="unadopt">
        Un-adopt<span class="tier">T1</span>
      </UiButton>
    </span>
  </div>
</template>

<style>
.skg {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.skg .st-meta {
  flex: 1 1 18rem;
  margin: 0;
}
.skg-acts {
  margin: 0;
}
.skl {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 8px;
}
.skl-acts {
  margin: 0;
}
.skl-vols {
  display: grid;
  gap: 6px;
}
.skg-copy .st-mono {
  overflow-wrap: anywhere;
}
</style>
