/**
 * A native modal `<dialog>` for edit mode's picker and tile editor: opened
 * on mount (focus moves in, the page behind is inert, Esc cancels), closed
 * on unmount. No motion library, so these chunks stay small.
 */
import { onBeforeUnmount, onMounted, type ShallowRef } from 'vue';

export function useModal(el: Readonly<ShallowRef<HTMLDialogElement | null>>): void {
  onMounted(() => {
    const d = el.value;
    if (!d || d.open) return;
    try {
      d.showModal();
    } catch {
      d.setAttribute('open', ''); // no showModal (older engines, test DOMs)
    }
  });
  onBeforeUnmount(() => {
    const d = el.value;
    if (d?.open) {
      try {
        d.close();
      } catch {
        d.removeAttribute('open');
      }
    }
  });
}
