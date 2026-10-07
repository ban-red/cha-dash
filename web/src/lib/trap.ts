/**
 * Keep Tab inside a dialog: from the last focusable element back to the
 * first, and from the first (with Shift) to the last. For dialogs that can't
 * be a native modal <dialog> (the motion-animated confirm).
 */
const FOCUSABLE = 'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

export function trapTab(e: KeyboardEvent, root: HTMLElement | null | undefined): void {
  if (e.key !== 'Tab' || !root) return;
  const items = [...root.querySelectorAll<HTMLElement>(FOCUSABLE)];
  if (!items.length) {
    e.preventDefault();
    return;
  }
  const first = items[0]!;
  const last = items[items.length - 1]!;
  const active = document.activeElement;
  if (!root.contains(active)) {
    e.preventDefault();
    first.focus();
  } else if (e.shiftKey && active === first) {
    e.preventDefault();
    last.focus();
  } else if (!e.shiftKey && active === last) {
    e.preventDefault();
    first.focus();
  }
}

/**
 * A modal layer is open above a sheet: the confirm, the step-up, or any
 * native modal <dialog>. Settings and the Inbox listen for Esc on the
 * document, so they check this before closing themselves.
 */
export function layerAbove(state: { confirm?: unknown; stepUp?: unknown }): boolean {
  if (state.confirm || state.stepUp) return true;
  return typeof document !== 'undefined' && document.querySelector('dialog[open], .layer-top:not(.inert)') !== null;
}
