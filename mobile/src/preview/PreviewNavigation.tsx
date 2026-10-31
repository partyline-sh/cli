import { router, useRootNavigationState } from 'expo-router';
import { useEffect } from 'react';
import { usePartyline } from '../runtime';
import { storePreview } from './config';
import target from './target.json';

/** The capture script selects a real route without simulated taps or an OS deep-link prompt. */
export function PreviewNavigation() {
  const root = useRootNavigationState();
  const app = usePartyline();
  useEffect(() => {
    if (!storePreview || !root?.key) return;
    const screen = target.screen;
    if (screen === 'messages' || screen === 'terminal') {
      app.selectView(screen);
      router.replace({ pathname: '/session/[computer]/[pane]', params: { computer: 'fixture-mbp', pane: '21' } });
    } else if (screen === 'loops' || screen === 'chat' || screen === 'computers') router.replace(`/${screen}`);
    else router.replace('/');
  }, [root?.key]); // eslint-disable-line react-hooks/exhaustive-deps
  return null;
}
