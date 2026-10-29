/**
 * Lists that scroll must be reachable by keyboard (WCAG 2.1.1; axe's
 * `scrollable-region-focusable`). A tile's `.list` scrolls only when it has
 * more rows than room, so it joins the tab order only then, marked
 * `data-scrolls`; a list that fits adds no tab stop.
 */

const LISTS = '.list';

/** Make each list under root focusable while it overflows. Returns the stop. */
export function focusScrollables(root: HTMLElement): () => void {
  const sync = () => {
    for (const el of root.querySelectorAll<HTMLElement>(LISTS)) {
      const scrolls = el.scrollHeight > el.clientHeight + 1;
      if (scrolls && !el.hasAttribute('data-scrolls')) {
        // "kept": the widget set its own tabindex, which stays.
        el.setAttribute('data-scrolls', el.hasAttribute('tabindex') ? 'kept' : '');
        if (!el.hasAttribute('tabindex')) el.tabIndex = 0;
      } else if (!scrolls && el.hasAttribute('data-scrolls')) {
        if (el.getAttribute('data-scrolls') !== 'kept') el.removeAttribute('tabindex');
        el.removeAttribute('data-scrolls');
      }
    }
  };
  sync();
  const ro = typeof ResizeObserver === 'function' ? new ResizeObserver(sync) : undefined;
  const watchLists = () => {
    if (!ro) return;
    ro.disconnect();
    ro.observe(root);
    for (const el of root.querySelectorAll<HTMLElement>(LISTS)) {
      ro.observe(el);
      // Rows coming and going change the scroll height, not the list's box.
      for (const row of el.children) ro.observe(row);
    }
  };
  watchLists();
  const mo = typeof MutationObserver === 'function' ? new MutationObserver(() => (watchLists(), sync())) : undefined;
  mo?.observe(root, { childList: true, subtree: true });
  return () => {
    ro?.disconnect();
    mo?.disconnect();
  };
}
