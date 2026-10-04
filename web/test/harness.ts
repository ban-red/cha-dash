/**
 * Mount components with the app's stores, fed by a fake hub socket carrying
 * the simulator snapshots.
 */
import { defineComponent, h, type Component } from 'vue';
import { mount } from '@vue/test-utils';
import { createLive, provideLive } from '../src/stores/live';
import { createSession, provideSession } from '../src/stores/session';
import { createUi, provideUi } from '../src/stores/ui';
import { FakeSocket, factory } from './fake-socket';

export function harness(data?: Record<string, unknown>) {
  FakeSocket.reset();
  const live = createLive({ url: 'ws://hub.test/ws', createSocket: factory, tick: false });
  const session = createSession();
  session.state.phase = 'ready';
  session.state.user = { id: 'u1', username: 'owner', role: 'owner' };
  const ui = createUi(live);
  live.start();
  const ws = FakeSocket.last;
  if (data) ws.handshake(data);

  const Root = defineComponent({
    setup(_, { slots }) {
      provideLive(live);
      provideUi(ui);
      provideSession(session);
      return () => slots['default']?.();
    },
  });

  function mountIn(component: Component, props: Record<string, unknown> = {}) {
    return mount(Root, { slots: { default: () => h(component, props) }, attachTo: document.body });
  }

  return { live, ui, session, ws, mountIn };
}
