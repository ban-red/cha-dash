/**
 * Mount components with the app's stores, fed by a fake hub socket carrying
 * the simulator snapshots.
 */
import { defineComponent, h, type Component } from 'vue';
import { mount } from '@vue/test-utils';
import { vi } from 'vitest';
import { createBoardStore, provideBoard } from '../src/stores/board';
import { createDiscovery, provideDiscovery } from '../src/stores/discovery';
import { createLive, provideLive } from '../src/stores/live';
import { createSession, provideSession } from '../src/stores/session';
import { createUi, provideUi } from '../src/stores/ui';
import { FakeSocket, factory } from './fake-socket';

export function harness(data?: Record<string, unknown>, opts: { demo?: boolean } = {}) {
  FakeSocket.reset();
  const live = createLive({ url: 'ws://hub.test/ws', createSocket: factory, tick: false });
  // GET /api/demo said demo mode: the Demo Home's clock; actions still run on the engine.
  live.demo.value = opts.demo === true;
  const session = createSession();
  session.state.phase = 'ready';
  session.state.user = { id: 'u1', username: 'owner', role: 'owner' };
  const signOut = vi.fn(async () => undefined);
  const ui = createUi(live, { signOut, role: () => session.state.user?.role, pollMs: 10 });
  const board = createBoardStore({ waitForGraph: async () => live.graph.value });
  const discovery = createDiscovery(live, board, () => ui.can.value.editBoards);
  live.start();
  const ws = FakeSocket.last;
  if (data) ws.handshake(data);

  const Root = defineComponent({
    setup(_, { slots }) {
      provideLive(live);
      provideUi(ui);
      provideSession(session);
      provideBoard(board);
      provideDiscovery(discovery);
      return () => slots['default']?.();
    },
  });

  function mountIn(component: Component, props: Record<string, unknown> = {}) {
    return mount(Root, { slots: { default: () => h(component, props) }, attachTo: document.body });
  }

  return { live, ui, session, board, discovery, ws, mountIn, signOut };
}
