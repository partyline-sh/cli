import { router, usePathname, useRootNavigationState } from 'expo-router';
import { useEffect, useRef } from 'react';
import { usePartyline } from '../runtime';
import { paneToRoute } from './route';

/** Mounted beside the root Stack, so returning to Sessions cannot replay startup. */
export function StartupNavigation() {
  const app = usePartyline();
  const pathname = usePathname();
  const root = useRootNavigationState();
  const restored = useRef(false);
  useEffect(() => {
    if (app.loading || !root?.key || restored.current) return;
    // Consume this even for an explicit deep link. Visiting / later is a user's
    // navigation, not another opportunity to restore the old session.
    restored.current = true;
    if (pathname !== '/') return;
    if (app.selected) router.replace({ pathname: '/session/[computer]/[pane]', params: {
      computer: app.selected.computerId, pane: paneToRoute(app.selected.pane),
    } });
    else if (app.tab !== 'sessions') router.replace(`/${app.tab}`);
  }, [app.loading, app.selected, app.tab, pathname, root?.key]);
  return null;
}
