<script setup lang="ts">
/**
 * A pane's explainer, folded. The one-line summary says what the pane is for;
 * "How it works" opens the long version. The long text stays in the page, so
 * search, screen readers and the tests find it either way.
 */
const { summary, label = 'How it works' } = defineProps<{ summary: string; label?: string }>();
</script>

<template>
  <details class="st-help">
    <summary>
      <span class="st-help-s">{{ summary }}</span>
      <span class="st-help-l">{{ label }}</span>
    </summary>
    <div class="st-lede"><slot /></div>
  </details>
</template>

<style>
.st-help > summary {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: var(--space-1) var(--space-3);
  max-width: 62ch;
  cursor: pointer;
  list-style: none;
  font-size: var(--type-small);
  line-height: var(--leading-relaxed);
  color: var(--text-2);
}
.st-help > summary::-webkit-details-marker {
  display: none;
}
.st-help-l {
  color: var(--text-3);
  text-decoration: underline;
  text-decoration-color: var(--line-2);
  text-underline-offset: 3px;
  white-space: nowrap;
}
.st-help > summary:hover .st-help-l {
  color: var(--text);
}
.st-help > summary:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 2px;
  border-radius: var(--radius-sm);
}
.st-help[open] .st-help-l::after {
  content: ' ▴';
}
.st-help:not([open]) .st-help-l::after {
  content: ' ▾';
}
.st-help > .st-lede {
  margin-top: var(--space-2);
  padding-left: var(--space-3);
  border-left: 2px solid var(--line-2);
}
</style>
