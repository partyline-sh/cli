import { useCallback } from 'react';
import { useFocusEffect } from 'expo-router';
import { usePartyline } from '../runtime';
import { AgentSessionScreen } from './AgentSessionScreen';
import { ShellSessionScreen } from './ShellSessionScreen';
import { isShellSession } from './terminal-layout';
import { paneFromRoute } from './route';

/** Route adapter only: all agent engines share one screen; shells stay separate. */
export function SessionScreen({ computer, pane: routePane }: { computer: string; pane: string }) {
  const pane = paneFromRoute(routePane);
  const app = usePartyline();
  useFocusEffect(useCallback(() => {
    if (app.loading) return;
    app.selectSession({ computerId: computer, pane });
    return () => app.selectSession(undefined);
  }, [computer, pane, app.loading])); // eslint-disable-line react-hooks/exhaustive-deps
  const session = app.sessions.find(s => s.computerId === computer && s.pane === pane);
  return session && isShellSession(session.engine)
    ? <ShellSessionScreen key={`${computer}:${pane}:shell`} computer={computer} pane={pane} />
    : <AgentSessionScreen key={`${computer}:${pane}:agent`} computer={computer} pane={pane} />;
}
