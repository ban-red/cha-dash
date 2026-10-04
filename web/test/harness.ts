/**
 * Mount components with the app's stores, fed by a fake hub socket carrying
 * the simulator snapshots.
 */
import { defineComponent, h, type Component } from 'vue';
import { mount } from '@vue/test-utils';
import { vi } from 'vitest';
import { createBoardStore, provideBoard } from '../src/stores/board';
import { createLive, provideLive } from '../src/stores/live';
import { createSession, provideSession } from '../src/stores/session';
import { createUi, provideUi } from '../src/stores/ui';
import { FakeSocket, factory } from './fake-socket';

export function harness(data?: Record<string, unknown>, opts: { demo?: boolean } = {}) {
  FakeSocket.reset();
  const live = createLive({ url: 'ws://hub.test/ws', createSocket: factory, tick: false });
  // GET /api/demo said demo mode: incidents' actions run through the demo Fix endpoint.
  live.demo.value = opts.demo === true;
  const session = createSession();
  session.state.phase = 'ready';
  session.state.user = { id: 'u1', username: 'owner', role: 'owner' };
  const signOut = vi.fn(async () => undefined);
  const ui = createUi(live, { signOut });
  const board = createBoardStore({ waitForGraph: async () => live.graph.value });
  live.start();
  const ws = FakeSocket.last;
  if (data) ws.handshake(data);

  const Root = defineComponent({
    setup(_, { slots }) {
      provideLive(live);
      provideUi(ui);
      provideSession(session);
      provideBoard(board);
      return () => slots['default']?.();
    },
  });

  function mountIn(component: Component, props: Record<string, unknown> = {}) {
    return mount(Root, { slots: { default: () => h(component, props) }, attachTo: document.body });
  }

  return { live, ui, session, board, ws, mountIn, signOut };
}
