import { useState } from 'react';
import { router } from 'expo-router';
import { KeyboardAvoidingView, Platform, Pressable, StyleSheet, Text, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { paneKey, usePartyline } from '../runtime';
import { agentDeliveryLabel, agentSessionAdapter, simpleConversation } from '../runtime/agent-session';
import { Composer } from './Composer';
import { Messages } from './Messages';
import { MotionGate } from './motion';
import { Empty } from './parts';
import { Terminal, terminalSizes } from './Terminal';
import { c, clock } from './theme';

const stateLabel = { waiting: 'waiting on you', working: 'working', idle: 'idle' } as const;

/** All LLM engines share this layout. Adapters describe data, not separate screens. */
export function AgentSessionScreen({ computer, pane }: { computer: string; pane: string }) {
  const app = usePartyline();
  const [size, setSize] = useState(1);
  const session = app.sessions.find(s => s.computerId === computer && s.pane === pane);
  const host = app.computers.find(h => h.id === computer);
  const snapshot = app.panes[paneKey(computer, pane)];
  const engine = session?.engine ?? 'the session';
  const adapter = agentSessionAdapter(engine);
  const deliveries = app.deliveries.filter(d => d.computerId === computer && d.pane === pane && d.target === session?.agentTarget);
  const conversation = simpleConversation(adapter, snapshot, deliveries);
  const view = app.sessionView;
  const received = view === 'messages' ? snapshot?.transcript?.receivedAt : snapshot?.terminal?.receivedAt;
  const stale = snapshot?.stale ? received ? `As of ${clock(received)}; reconnecting` : 'Reconnecting' : undefined;
  return <MotionGate active={host?.connection === 'connected' && !snapshot?.stale}>
    <SafeAreaView edges={['top', 'bottom']} style={s.screen}>
      <View style={s.bar}>
        <Pressable accessibilityRole="button" accessibilityLabel="Back" hitSlop={12} onPress={() => router.canGoBack() ? router.back() : router.replace('/')}><Text style={s.back}>‹</Text></Pressable>
        <View style={s.title}>
          <Text numberOfLines={1} style={s.name}>{session?.title ?? pane}</Text>
          <Text numberOfLines={1} style={s.where}>{[session?.engine, host?.name, session ? stateLabel[session.state] : 'loading'].filter(Boolean).join(' · ')}</Text>
        </View>
        {view === 'terminal' ? <Pressable accessibilityRole="button" accessibilityLabel="Text size" hitSlop={8} style={s.size} onPress={() => setSize(value => (value + 1) % terminalSizes)}><Text style={s.sizeText}>Aa</Text></Pressable> : <View style={s.spacer} />}
      </View>
      <View accessibilityRole="tablist" style={s.toggle}>
        {(['messages', 'terminal'] as const).map(value => <Pressable key={value} accessibilityRole="tab" accessibilityLabel={value === 'messages' ? 'Simple' : 'Session'} accessibilityState={{ selected: value === view }} onPress={() => app.selectView(value)} style={[s.tab, value === view && s.tabOn]}>
          <Text style={[s.tabText, value === view && { color: c.text }]}>{value === 'messages' ? 'Simple' : 'Session'}</Text>
        </Pressable>)}
      </View>
      <KeyboardAvoidingView style={s.body} behavior={Platform.OS === 'ios' ? 'padding' : 'height'}>
        {view === 'messages' ? <View style={s.body}>
          {conversation.notice ? <Text accessibilityLiveRegion="polite" style={s.notice}>{conversation.notice}</Text> : null}
          <Messages entries={conversation.entries} stale={stale} />
        </View> : snapshot?.terminal ? <View style={s.body}>
          {stale ? <Text style={s.notice}>{stale}</Text> : null}
          <Terminal ansi={snapshot.terminal.ansi} columns={snapshot.terminal.columns} size={size} defaultDisplay="wrap" showControls={false} />
        </View> : <View style={s.body}><Empty>{host?.connection === 'connected' ? 'Loading session…' : `Waiting for ${host?.name ?? 'the computer'} to connect.`}</Empty></View>}
        <Composer key={session?.agentTarget ?? 'managed'} shell={false} computerId={computer} pane={pane} placeholder={`Message ${engine}…`} deliveryLabel={delivery => agentDeliveryLabel(adapter, delivery, snapshot, host?.connection === 'connected' && session?.state === 'working')} />
      </KeyboardAvoidingView>
    </SafeAreaView>
  </MotionGate>;
}

const s = StyleSheet.create({
  screen: { flex: 1, backgroundColor: c.ground }, body: { flex: 1 },
  bar: { flexDirection: 'row', alignItems: 'center', gap: 8, paddingHorizontal: 12, paddingBottom: 8, borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: c.line },
  back: { color: c.accent, fontSize: 32, lineHeight: 34, width: 28 },
  title: { flex: 1, alignItems: 'center', minWidth: 0 }, name: { color: c.text, fontSize: 16, fontWeight: '700' }, where: { color: c.muted, fontSize: 12 },
  size: { width: 36, height: 36, borderRadius: 18, backgroundColor: c.raised, alignItems: 'center', justifyContent: 'center' }, spacer: { width: 36 }, sizeText: { color: c.text, fontSize: 13, fontWeight: '600' },
  toggle: { flexDirection: 'row', alignSelf: 'center', backgroundColor: c.raised, borderRadius: 9, padding: 2, marginTop: 8 },
  tab: { paddingHorizontal: 14, paddingVertical: 10, minHeight: 44, borderRadius: 7, justifyContent: 'center' }, tabOn: { backgroundColor: c.surface }, tabText: { color: c.muted, fontSize: 12, fontWeight: '600' },
  notice: { color: c.muted, fontSize: 12, padding: 10, textAlign: 'center' },
});
