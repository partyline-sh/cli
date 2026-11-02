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
    if (screen === 'stream') router.replace({pathname:'/terminal/[computer]/[session]',params:{computer:'fixture-mbp',session:'new'}});
    else if (screen === 'messages' || screen === 'terminal' || screen === 'shell') {
      app.selectView(screen === 'shell' ? 'terminal' : screen);
      router.replace({ pathname: '/session/[computer]/[pane]', params: { computer: 'fixture-mbp', pane: screen === 'shell' ? '77' : '21' } });
    } else if (screen === 'loops' || screen === 'chat' || screen === 'computers') router.replace(`/${screen}`);
    else router.replace('/');
  }, [root?.key]); // eslint-disable-line react-hooks/exhaustive-deps
  return null;
}
