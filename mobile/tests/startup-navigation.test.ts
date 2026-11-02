import { createElement, Fragment } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { StartupNavigation } from '../src/ui/StartupNavigation';
import SessionsRoute from '../src/app/(tabs)/index';

const nav = vi.hoisted(() => ({
  replace: vi.fn(), path: '/', ready: true,
  app: { loading: false, tab: 'sessions', selected: undefined as undefined | { computerId: string; pane: string } },
}));
vi.mock('expo-router', () => ({
  router: { replace: nav.replace }, usePathname: () => nav.path,
  useRootNavigationState: () => nav.ready ? { key: 'root' } : undefined,
}));
vi.mock('../src/runtime', () => ({ usePartyline: () => nav.app }));
vi.mock('../src/ui', () => ({ SessionsScreen: () => null }));


let rendered: ReactTestRenderer | undefined;
// These components exercise React effects without a native renderer or network.
function Root({ visit, list = true }: { visit: number; list?: boolean }) {
  return createElement(Fragment, null, createElement(StartupNavigation), list ? createElement(SessionsRoute, { key: visit }) : null);
}
const render = async (visit = 0, list = true) => act(async () => {
  const element = createElement(Root, { visit, list });
  if (rendered) rendered.update(element); else rendered = create(element);
});
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  nav.replace.mockReset(); nav.path = '/'; nav.ready = true;
  nav.app = { loading: false, tab: 'sessions', selected: undefined };
});
afterEach(async () => { await act(async () => rendered?.unmount()); rendered = undefined; });

describe('startup navigation and returning to the session list', () => {
  it('restores once, then leaves the remounted list alone even with stale selection and new activity', async () => {
    nav.app.selected = { computerId: 'mac', pane: '%21' };
    await render();
    expect(nav.replace).toHaveBeenCalledExactlyOnceWith({ pathname: '/session/[computer]/[pane]', params: { computer: 'mac', pane: '21' } });
    nav.path = '/session/mac/21';
    await render(0, false);
    // Back mounts a fresh Sessions route before the old session's cleanup has
    // necessarily cleared selection. A route-local ref used to redirect again.
    nav.path = '/';
    await render(1);
    nav.app.selected = { computerId: 'mac', pane: '%32' };
    await render(2);
    expect(nav.replace).toHaveBeenCalledTimes(1);
  });

  it('does not hijack explicit navigation or restore an old session on a later root visit', async () => {
    nav.path = '/session/mac/9';
    nav.app.selected = { computerId: 'mac', pane: '%21' };
    await render(0, false);
    nav.path = '/'; await render(1);
    nav.path = '/chat'; await render(1, false);
    nav.path = '/'; await render(2);
    expect(nav.replace).not.toHaveBeenCalled();
  });

  it('restores a saved tab once without pulling later Sessions visits back to it', async () => {
    nav.app.tab = 'chat';
    await render();
    expect(nav.replace).toHaveBeenCalledExactlyOnceWith('/chat');
    nav.path = '/chat'; await render(0, false);
    nav.path = '/'; await render(1);
    expect(nav.replace).toHaveBeenCalledTimes(1);
  });

  it('waits for storage and the root navigator before consuming startup', async () => {
    nav.app.loading = true; nav.ready = false; nav.app.tab = 'loops';
    await render(); nav.ready = true; await render();
    expect(nav.replace).not.toHaveBeenCalled();
    nav.app.loading = false; await render();
    expect(nav.replace).toHaveBeenCalledExactlyOnceWith('/loops');
  });
});
