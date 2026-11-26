import { useState } from 'react';
import { router } from 'expo-router';
import { KeyboardAvoidingView, Platform, Pressable, StyleSheet, Text, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { paneKey, usePartyline } from '../runtime';
import { Composer } from './Composer';
import { MotionGate } from './motion';
import { Empty } from './parts';
import { Signal } from './Signal';
import { Terminal, terminalSizes } from './Terminal';
import { c, clock } from './theme';
import { savedViewLabel } from './HistoryStatus';

const stateLabel = { waiting: 'waiting on you', working: 'working', idle: 'idle' } as const;

/** Every LLM uses the same terminal and process-bound message composer. */
export function AgentSessionScreen({ computer, pane }: { computer: string; pane: string }) {
  const app = usePartyline();
  const [size, setSize] = useState(1);
  const session = app.sessions.find(s => s.computerId === computer && s.pane === pane);
  const host = app.computers.find(h => h.id === computer);
  const snapshot = app.panes[paneKey(computer, pane)];
  const engine = session?.engine ?? 'the session';
  const received = snapshot?.terminal?.receivedAt;
  const connected = host?.connection === 'connected';
  const refreshing = connected && (!received || snapshot?.stale) && !snapshot?.refreshError;
  const status = snapshot?.cached && received ? savedViewLabel(received, connected) + (snapshot.refreshError ? ` · ${snapshot.refreshError}` : '') : !connected ? `Waiting for ${host?.name ?? 'the computer'} to reconnect` : snapshot?.refreshError
    ?? (refreshing ? 'Refreshing session…' : received ? `Updated ${clock(received)}` : 'Loading session…');
  return <MotionGate active={connected && !snapshot?.cached}>
    <SafeAreaView edges={['top', 'bottom']} style={s.screen}>
      <View style={s.bar}>
        <Pressable accessibilityRole="button" accessibilityLabel="Back" hitSlop={12} onPress={() => router.canGoBack() ? router.back() : router.replace('/')}><Text style={s.back}>‹</Text></Pressable>
        <View style={s.title}>
          <Text numberOfLines={1} style={s.name}>{session?.title ?? pane}</Text>
          <Text numberOfLines={1} style={s.where}>{[session?.engine, host?.name, session?.cached ? 'saved' : session ? stateLabel[session.state] : 'loading'].filter(Boolean).join(' · ')}</Text>
        </View>
        <Pressable accessibilityRole="button" accessibilityLabel="Text size" hitSlop={8} style={s.size} onPress={() => setSize(value => (value + 1) % terminalSizes)}><Text style={s.sizeText}>Aa</Text></Pressable>
      </View>
      <KeyboardAvoidingView style={s.body} behavior={Platform.OS === 'ios' ? 'padding' : 'height'}>
        <View style={s.refresh} accessibilityLiveRegion="polite">
          <Signal mode={refreshing ? 'active' : !connected ? 'offline' : snapshot?.refreshError ? 'blocked' : 'confirmed'} />
          <Text style={s.refreshText}>{status}{received && !snapshot?.cached && (refreshing || !connected || snapshot?.refreshError) ? ` · showing output from ${clock(received)}` : ''}</Text>
          {connected && app.refreshSession ? <Pressable accessibilityRole="button" accessibilityLabel="Refresh session output" disabled={refreshing}
            onPress={() => app.refreshSession?.(computer, pane)} style={s.refreshButton}><Text style={[s.refreshAction, refreshing && { opacity: .4 }]}>{snapshot?.refreshError ? 'Retry' : 'Refresh'}</Text></Pressable> : null}
        </View>
        {snapshot?.terminal ? <View style={s.body}>
          <Terminal ansi={snapshot.terminal.ansi} columns={snapshot.terminal.columns} size={size} defaultDisplay="wrap" showControls={false} />
        </View> : <View style={s.body}><Empty>{connected ? 'Fetching the current terminal screen…' : `Waiting for ${host?.name ?? 'the computer'} to connect.`}</Empty></View>}
        <Composer key={session?.agentTarget ?? 'managed'} shell={false} computerId={computer} pane={pane} placeholder={`Message ${engine}…`} />
      </KeyboardAvoidingView>
    </SafeAreaView>
  </MotionGate>;
}

const s = StyleSheet.create({
  screen: { flex: 1, backgroundColor: c.ground }, body: { flex: 1 },
  bar: { flexDirection: 'row', alignItems: 'center', gap: 8, paddingHorizontal: 12, paddingBottom: 8, borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: c.line },
  back: { color: c.accent, fontSize: 32, lineHeight: 34, width: 28 },
  title: { flex: 1, alignItems: 'center', minWidth: 0 }, name: { color: c.text, fontSize: 16, fontWeight: '700' }, where: { color: c.muted, fontSize: 12 },
  size: { width: 36, height: 36, borderRadius: 18, backgroundColor: c.raised, alignItems: 'center', justifyContent: 'center' }, sizeText: { color: c.text, fontSize: 13, fontWeight: '600' },
  refresh: { flexDirection: 'row', alignItems: 'center', gap: 6, paddingHorizontal: 10, minHeight: 34 },
  refreshText: { flex: 1, color: c.muted, fontSize: 11 },
  refreshButton: { paddingHorizontal: 8, paddingVertical: 9 },
  refreshAction: { color: c.accent, fontSize: 12 },
});
