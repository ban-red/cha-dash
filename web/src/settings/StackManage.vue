<script setup lang="ts">
/**
 * One stack's place under Cha Dash: where a moved stack came from (while that
 * copy is still there, the tool that wrote it may still deploy it), Move for a
 * stack adopted in place, and Un-adopt (`stack.unadopt`, T1 with Undo), which
 * stops versioning it and leaves its files and containers as they are.
 *
 * Shown in the stack's page (StackEditor.vue) under the file's path.
 */
import { computed } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import UiButton from '../ui/Button.vue';
import StackMove from './StackMove.vue';
import { useUi } from '../stores/ui';
import { targetOf, type StackDetail } from './stacks';

const props = defineProps<{ detail: StackDetail; /** A task is running on the stack. */ busy?: boolean }>();

const ui = useUi();
const canAct = computed(() => ui.can.value.act('T1'));
const managed = computed(() => props.detail.managed && !props.detail.protected);
const inPlace = computed(() => managed.value && props.detail.location === 'external');

function unadopt(): void {
  void ui.runAction({ action: 'stack.unadopt', target: targetOf(props.detail.node, props.detail.name), label: `Un-adopt ${props.detail.name}`, tier: 'T1' });
}
</script>

<template>
  <div v-if="detail.moved_from" class="st-note attn skg-copy" role="note">
    <Glyph kind="attn" />
    <span>A copy stays at <span class="st-mono">{{ detail.moved_from }}</span>; the tool that wrote it may still deploy it.</span>
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
.skg-copy .st-mono {
  overflow-wrap: anywhere;
}
</style>
