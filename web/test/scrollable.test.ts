/**
 * A tile's list that scrolls joins the tab order, and only while it scrolls
 * (axe's scrollable-region-focusable): lib/scrollable.ts.
 */
import { afterEach, describe, expect, it } from 'vitest';
import { nextTick } from 'vue';
import { focusScrollables } from '../src/lib/scrollable';

/** jsdom has no layout: give a list the heights a browser would. */
function sized(el: HTMLElement, scroll: number, client: number): void {
  Object.defineProperty(el, 'scrollHeight', { configurable: true, get: () => scroll });
  Object.defineProperty(el, 'clientHeight', { configurable: true, get: () => client });
}

afterEach(() => {
  document.body.innerHTML = '';
});

describe('focusScrollables', () => {
  it('makes a list that overflows focusable, and leaves one that fits alone', () => {
    document.body.innerHTML = '<article><ul class="list" aria-label="Queues"><li>a</li></ul><ul class="list"><li>b</li></ul></article>';
    const [long, short] = [...document.querySelectorAll<HTMLElement>('.list')];
    sized(long!, 300, 120);
    sized(short!, 80, 120);
    const stop = focusScrollables(document.querySelector('article')!);
    expect(long!.getAttribute('tabindex')).toBe('0');
    expect(long!.hasAttribute('data-scrolls')).toBe(true);
    expect(short!.hasAttribute('tabindex')).toBe(false);
    stop();
  });

  it('follows rows coming and going', async () => {
    document.body.innerHTML = '<article><ul class="list"><li>a</li></ul></article>';
    const list = document.querySelector<HTMLElement>('.list')!;
    let rows = 1;
    sized(list, 0, 120);
    Object.defineProperty(list, 'scrollHeight', { configurable: true, get: () => rows * 50 });
    const stop = focusScrollables(document.querySelector('article')!);
    expect(list.hasAttribute('tabindex')).toBe(false);
    rows = 5;
    list.insertAdjacentHTML('beforeend', '<li>b</li><li>c</li><li>d</li><li>e</li>');
    await nextTick();
    await new Promise((r) => setTimeout(r));
    expect(list.getAttribute('tabindex')).toBe('0');
    rows = 1;
    list.replaceChildren(document.createElement('li'));
    await new Promise((r) => setTimeout(r));
    expect(list.hasAttribute('tabindex')).toBe(false);
    stop();
  });

  it('keeps a tabindex the widget set itself', () => {
    document.body.innerHTML = '<article><ul class="list" tabindex="-1"><li>a</li></ul></article>';
    const list = document.querySelector<HTMLElement>('.list')!;
    sized(list, 300, 120);
    const stop = focusScrollables(document.querySelector('article')!);
    expect(list.getAttribute('tabindex')).toBe('-1');
    sized(list, 80, 120);
    list.append(document.createElement('li'));
    return new Promise<void>((done) =>
      setTimeout(() => {
        expect(list.getAttribute('tabindex')).toBe('-1');
        expect(list.hasAttribute('data-scrolls')).toBe(false);
        stop();
        done();
      }),
    );
  });
});
