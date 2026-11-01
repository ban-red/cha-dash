<script setup lang="ts">
/**
 * The Atlas as a list: the same nodes as the canvas, in its order, grouped
 * by the layer each root belongs to and nested as the map nests them (what
 * runs on what). Each is a button with its health and first cause in
 * words: the way keyboard and screen-reader users read the map.
 */
import { computed, defineComponent, h, type PropType, type VNode } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import { glyphFor } from '../model/status';
import { KIND_WORDS, LAYERS, type Atlas, type AtlasNode } from '../model/atlas';
import type { CauseHint } from '../model/types';

const { atlas, selected, causesOf } = defineProps<{
  atlas: Atlas;
  selected?: string | undefined;
  causesOf: (id: string) => CauseHint[];
}>();
const emit = defineEmits<{ open: [id: string]; expand: [group: string] }>();

const WORDS: Record<string, string> = { ok: 'ok', attention: 'needs attention', down: 'down', maintenance: 'maintenance', repair: 'needs repair', unknown: 'no reading' };

const sections = computed(() =>
  LAYERS.map((l) => ({ ...l, roots: atlas.roots.filter((r) => atlas.nodes.get(r)!.layer === l.id) })).filter((s) => s.roots.length),
);

function because(id: string): string | undefined {
  const c = causesOf(id)[0];
  if (!c) return undefined;
  const what = typeof c.args?.['what'] === 'string' ? c.args['what'] : c.text;
  return `because ${what.charAt(0).toLowerCase()}${what.slice(1)}`;
}

/** One node and what hangs under it, recursively. */
const Item = defineComponent({
  name: 'AtlasItem',
  props: { node: { type: Object as PropType<AtlasNode>, required: true } },
  setup(props): () => VNode {
    return () => {
      const n = props.node;
      const word = n.folded ? 'folded' : (WORDS[n.health] ?? n.health);
      const why = n.folded || n.health === 'ok' ? undefined : because(n.id);
      const kids = n.children.map((c) => atlas.nodes.get(c)!);
      return h('li', [
        h(
          'button',
          {
            type: 'button',
            class: 'atlas-item',
            'data-id': n.id,
            'aria-current': n.id === selected ? 'true' : undefined,
            onClick: () => (n.folded ? emit('expand', n.id) : emit('open', n.id)),
          },
          [
            h(Glyph, { kind: n.folded ? 'prog' : glyphFor(n.health) }),
            h('span', [
              n.label,
              h('span', { class: 'what' }, ` · ${n.folded ? 'show them' : (KIND_WORDS[n.kind] ?? n.kind)} · ${word}${n.reason && n.health !== 'ok' ? `: ${n.reason}` : ''}`),
            ]),
            why ? h('span', { class: 'why' }, why) : null,
          ],
        ),
        kids.length ? h('ul', kids.map((k) => h(Item, { key: k.id, node: k }))) : null,
      ]);
    };
  },
});
</script>

<template>
  <nav class="atlas-list" aria-label="Atlas as a list">
    <template v-for="s in sections" :key="s.id">
      <h3>{{ s.name }}</h3>
      <ul>
        <Item v-for="r in s.roots" :key="r" :node="atlas.nodes.get(r)!" />
      </ul>
    </template>
    <p v-if="!sections.length" class="muted">Nothing to show with these layers.</p>
  </nav>
</template>
