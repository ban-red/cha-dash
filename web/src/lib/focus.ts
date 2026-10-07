/**
 * Moving keyboard focus on purpose, after Vue has rendered.
 *
 * When a control a keyboard user is on goes away (a confirm group closes, a
 * row is removed, a form is cancelled), the browser drops focus to <body>
 * and they start again from the top. These put it somewhere that makes sense.
 */
import { nextTick } from 'vue';

type Target = string | HTMLElement | null | undefined;

/** The element for a target: an element, or an element id. */
function resolve(t: Target): HTMLElement | null {
  if (!t) return null;
  return typeof t === 'string' ? document.getElementById(t) : t;
}

/**
 * After the next render, focus the first target that exists and is
 * connected: element ids or elements, in order of preference. A heading
 * used as a fallback needs tabindex="-1". Resolves whether anything took
 * focus.
 */
export async function focusFirst(...targets: Target[]): Promise<boolean> {
  await nextTick();
  for (const t of targets) {
    const el = resolve(t);
    if (el && el.isConnected) {
      el.focus();
      if (document.activeElement === el) return true;
    }
  }
  return false;
}

/** Is focus inside (or on) this element? */
export function hasFocus(el: Element | null | undefined): boolean {
  const a = document.activeElement;
  return !!el && !!a && (el === a || el.contains(a));
}
