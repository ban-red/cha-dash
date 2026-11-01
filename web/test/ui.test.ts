import { afterEach, describe, expect, it } from 'vitest';
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils';
import { defineComponent, h, nextTick, ref } from 'vue';
import UiButton from '../src/ui/Button.vue';
import UiCloseButton from '../src/ui/CloseButton.vue';
import UiDialog from '../src/ui/Dialog.vue';
import UiField from '../src/ui/Field.vue';
import UiMenu from '../src/ui/Menu.vue';
import UiCatalog from '../src/dev/UiCatalog.vue';
import { axeViolations } from './axe';

enableAutoUnmount(afterEach);
afterEach(() => {
  document.body.innerHTML = '';
});

describe('UiButton', () => {
  it('is a type="button" with the .btn classes for its variant and size', () => {
    const w = mount(UiButton, { props: { variant: 'danger-solid', size: 'sm' }, slots: { default: 'Delete' } });
    const b = w.get('button');
    expect(b.attributes('type')).toBe('button');
    expect(b.classes()).toEqual(['btn', 'danger', 'solid', 'sm']);
    expect(b.text()).toBe('Delete');
  });

  it('is a link with href, and passes attributes and listeners through', async () => {
    let clicks = 0;
    const w = mount(UiButton, { props: { href: '/settings' }, attrs: { target: '_blank', onClick: () => clicks++ }, slots: { default: 'Open' } });
    const a = w.get('a');
    expect(a.attributes('href')).toBe('/settings');
    expect(a.attributes('target')).toBe('_blank');
    expect(a.attributes('type')).toBeUndefined();
    await a.trigger('click');
    expect(clicks).toBe(1);
  });

  it('exposes focus()', () => {
    const w = mount(UiButton, { attachTo: document.body, slots: { default: 'Go' } });
    w.vm.focus();
    expect(document.activeElement).toBe(w.element);
  });
});

describe('UiCloseButton', () => {
  it('names the × for what it does', () => {
    expect(mount(UiCloseButton).get('button.x').attributes('aria-label')).toBe('Close');
    expect(mount(UiCloseButton, { props: { label: 'Dismiss' } }).get('button').attributes('aria-label')).toBe('Dismiss');
  });
});

describe('UiField', () => {
  const field = (props: Record<string, unknown>, slots: Record<string, string> = {}) =>
    mount(UiField, {
      props,
      slots: { default: `<template #default="{ control }"><input v-bind="control" /></template>`, ...slots },
    });

  it('ties the label and the help to the control', () => {
    const w = field({ label: 'Name', help: 'What you call it.' });
    const input = w.get('input');
    const id = input.attributes('id')!;
    expect(w.get('label').attributes('for')).toBe(id);
    expect(w.get(`#${input.attributes('aria-describedby')}`).text()).toBe('What you call it.');
    expect(input.attributes('aria-invalid')).toBeUndefined();
  });

  it('shows the error instead of the help, marks the control invalid, and announces it with alert', () => {
    const w = field({ label: 'Port', help: 'A number.', error: 'Not a number.', alert: true });
    const help = w.get('.help');
    expect(help.text()).toBe('Not a number.');
    expect(help.classes()).toContain('bad');
    expect(help.attributes('role')).toBe('alert');
    expect(w.get('input').attributes('aria-invalid')).toBe('true');
  });

  it('keeps the help but turns it bad when invalid', () => {
    const w = field({ label: 'Password', help: 'At least 6 characters.', invalid: true });
    expect(w.get('.help').text()).toBe('At least 6 characters.');
    expect(w.get('.help').classes()).toContain('bad');
    expect(w.get('input').attributes('aria-invalid')).toBe('true');
  });

  it('uses a given id, and has no describedby without a message', () => {
    const w = field({ label: 'User', id: 'login-user' });
    expect(w.get('input').attributes('id')).toBe('login-user');
    expect(w.get('input').attributes('aria-describedby')).toBeUndefined();
    expect(w.find('.help').exists()).toBe(false);
  });

  it('puts a checkbox inside its label, and can hide a label for screen readers only', () => {
    const c = field({ label: 'Include unmonitored', check: true });
    expect(c.get('.field').classes()).toContain('check');
    expect(c.find('label input').exists()).toBe(true);
    const s = field({ label: 'Filter', hideLabel: true });
    expect(s.get('label').classes()).toContain('sr');
  });

  it('has an optional word and an aside in the label row', () => {
    const w = field({ label: 'Token', optional: true }, { aside: '<span class="set-chip">set</span>' });
    expect(w.get('.label-row .set-chip').text()).toBe('set');
    expect(w.get('.label-row .opt-word').text()).toBe('optional');
  });
});

describe('UiDialog', () => {
  it('opens as a modal, is named by its title, and emits close from Esc and the ×', async () => {
    const closes: string[] = [];
    const w = mount(UiDialog, { attachTo: document.body, props: { title: 'Dismissed services', onClose: () => closes.push('x') }, slots: { default: '<p>Body</p>', foot: '<button>Done</button>' } });
    const d = w.get('dialog');
    expect(d.element.hasAttribute('open')).toBe(true);
    expect(d.get(`#${d.attributes('aria-labelledby')}`).text()).toBe('Dismissed services');
    expect(d.get('.dlg-foot button').text()).toBe('Done');
    await d.trigger('cancel');
    await w.get('.dlg-head .x').trigger('click');
    expect(closes).toEqual(['x', 'x']);
  });

  it('as a form, emits submit, and requestSubmit() submits it', async () => {
    let submits = 0;
    const Host = defineComponent({
      setup() {
        const dlg = ref<InstanceType<typeof UiDialog> | null>(null);
        return () => h(UiDialog, { ref: dlg, as: 'form', title: 'Add', onSubmit: () => submits++ }, { foot: () => h('button', { type: 'submit' }, 'Add') });
      },
    });
    const w = mount(Host, { attachTo: document.body });
    expect(w.find('form.dlg-body').exists()).toBe(true);
    await w.get('form').trigger('submit');
    expect(submits).toBe(1);
  });

  it('has a full size whose body fills it', () => {
    const w = mount(UiDialog, { attachTo: document.body, props: { title: 'Atlas', size: 'full' } });
    expect(w.get('dialog').classes()).toEqual(['dlg', 'dlg-full']);
    w.unmount();
  });

  it('closes the native dialog when it unmounts', async () => {
    const show = ref(true);
    const w = mount(defineComponent({ setup: () => () => (show.value ? h(UiDialog, { title: 'T' }) : null) }), { attachTo: document.body });
    const d = w.get('dialog').element as HTMLDialogElement;
    show.value = false;
    await nextTick();
    expect(d.open).toBe(false);
  });
});

describe('UiMenu', () => {
  /** A menu with an item, a submenu of two radios and a confirm, like the tile menu. */
  const Host = defineComponent({
    setup() {
      const picked = ref('');
      return () =>
        h('div', [
          h('span', { id: 'picked' }, picked.value),
          h(
            UiMenu,
            { label: 'Tile menu', class: 'cog' },
            {
              default: ({ view, goto, close }: { view: string; goto: (v: string) => void; close: (refocus?: boolean) => void }) =>
                view === 'menu'
                  ? h('div', { role: 'menu', 'aria-label': 'Tile menu' }, [
                      h('button', { type: 'button', role: 'menuitem', class: 'menu-item', tabindex: -1, onClick: () => ((picked.value = 'one'), close(true)) }, 'One'),
                      h('button', { type: 'button', role: 'menuitem', class: 'menu-item', tabindex: -1, onClick: () => goto('size') }, 'Size'),
                      h('button', { type: 'button', role: 'menuitem', class: 'menu-item', tabindex: -1 }, 'Three'),
                    ])
                  : h('div', { role: 'menu', 'aria-label': 'Size' }, [
                      h('button', { type: 'button', role: 'menuitemradio', class: 'menu-item', tabindex: -1, 'aria-checked': false }, 'S'),
                      h('button', { type: 'button', role: 'menuitemradio', class: 'menu-item', tabindex: -1, 'aria-checked': true }, 'M'),
                    ]),
            },
          ),
        ]);
    },
  });
  const pop = () => document.querySelector<HTMLElement>('.menu-pop');
  const items = () => [...document.querySelectorAll<HTMLElement>('.menu-pop [role^="menuitem"]')];
  const key = (k: string) => document.activeElement!.dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true }));

  it('opens from its trigger in <body>, focused on the first item, and arrows wrap', async () => {
    const w = mount(Host, { attachTo: document.body });
    const trigger = w.get('button.cog');
    expect(trigger.attributes()).toMatchObject({ 'aria-haspopup': 'menu', 'aria-expanded': 'false', 'aria-label': 'Tile menu' });
    await trigger.trigger('click');
    await flushPromises();
    expect(pop()?.parentElement).toBe(document.body);
    expect(trigger.attributes('aria-expanded')).toBe('true');
    expect(document.activeElement).toBe(items()[0]);
    expect(await axeViolations(document.body, ['region'])).toEqual([]);
    key('ArrowUp');
    expect(document.activeElement).toBe(items()[2]);
    key('Home');
    expect(document.activeElement).toBe(items()[0]);
    key('End');
    expect(document.activeElement).toBe(items()[2]);
    items()[0]!.click();
    await flushPromises();
    expect(pop()).toBeNull();
    expect(w.get('#picked').text()).toBe('one');
    expect(document.activeElement).toBe(trigger.element);
  });

  it('goes to a view on the checked item, and Escape steps back to the item that opened it, then closes', async () => {
    const w = mount(Host, { attachTo: document.body });
    await w.get('button.cog').trigger('click');
    await flushPromises();
    items()[1]!.click();
    await flushPromises();
    expect(items().map((e) => e.textContent)).toEqual(['S', 'M']);
    expect(document.activeElement).toBe(items()[1]);
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    await flushPromises();
    expect(document.activeElement?.textContent).toBe('Size');
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    await flushPromises();
    expect(pop()).toBeNull();
  });

  it('closes on a click outside and on Tab', async () => {
    const w = mount(Host, { attachTo: document.body });
    const trigger = w.get('button.cog');
    await trigger.trigger('click');
    await flushPromises();
    document.body.dispatchEvent(new Event('pointerdown', { bubbles: true }));
    await flushPromises();
    expect(pop()).toBeNull();
    await trigger.trigger('click');
    await flushPromises();
    key('Tab');
    await flushPromises();
    expect(pop()).toBeNull();
  });
});

describe('the catalog', () => {
  it('renders every primitive, and passes axe', async () => {
    mount(UiCatalog, { attachTo: document.body });
    await flushPromises();
    expect(document.querySelectorAll('.cat .btn').length).toBeGreaterThan(10);
    expect(document.querySelectorAll('.cat .field').length).toBeGreaterThan(5);
    expect(await axeViolations(document.body, ['region'])).toEqual([]);
  });
});
