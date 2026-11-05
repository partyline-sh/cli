import { MotionGate } from './motion';
import { router } from 'expo-router';
import { useCallback, useState } from 'react';
import { KeyboardAvoidingView, Platform, Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { paneKey, usePartyline } from '../runtime';
import type { LiveScreen } from '../runtime/live-terminal';
import { CloseSessionButton } from './CloseSessionButton';
import { TerminalHistoryButton } from './TerminalHistoryButton';
import { LiveTerminal } from './LiveTerminal';
import { Composer } from './Composer';
import { Terminal, terminalSizes } from './Terminal';
import { Empty } from './parts';
import { paneFromRoute } from './route';
import { c, clock } from './theme';
import { TerminalLayoutControls } from './TerminalLayoutControls';
import { type TerminalViewport } from './terminal-layout';
import { savedViewLabel } from './HistoryStatus';

const stateLabel = { waiting: 'waiting on you', working: 'working', idle: 'idle' } as const;

/** Legacy desktop shells retain their live typing, display controls and fallback. */
export function ShellSessionScreen({ computer, pane: routePane }: { computer: string; pane: string }) {
  const pane = paneFromRoute(routePane);
  const app = usePartyline();
  const [size, setSize] = useState(1);
  const [viewport, setViewport] = useState<TerminalViewport>();
  const [displayOptions, setDisplayOptions] = useState(false);
  const [showSavedDrafts, setShowSavedDrafts] = useState(false);
  const draftPrefix = `${paneKey(computer, pane)}@`;
  const savedDrafts = Object.entries(app.drafts ?? {}).filter(([key, text]) => key.startsWith(draftPrefix + 'agent:') && text.trim());
  const [failure, setFailure] = useState<{ key: string; reason: string; screen?: LiveScreen; at: number }>();
  const sessionKey = `${computer}:${pane}`;
  const fallback = failure?.key === sessionKey ? failure : undefined;
  const liveClosed = useCallback((reason: string, screen?: LiveScreen) => setFailure({ key: sessionKey, reason, screen, at: Date.now() }), [sessionKey, setFailure]);
  const session = app.sessions.find(s => s.computerId === computer && s.pane === pane);
  const host = app.computers.find(k => k.id === computer);
  const snap = app.panes[paneKey(computer, pane)];
  const asOf = snap?.terminal?.receivedAt;
  const stale = snap?.cached && asOf ? savedViewLabel(asOf, host?.connection === 'connected') + (snap.refreshError ? ` · ${snap.refreshError}` : '') : snap?.stale && asOf ? `As of ${clock(asOf)}; reconnecting` : undefined;
  const live = !session?.cached && !snap?.cached && !fallback && Platform.OS !== "web" && !!host?.capabilities.includes("live_terminal") && !!app.openLiveTerminal;

  return <MotionGate active={host?.connection === 'connected' && !snap?.stale}><SafeAreaView edges={['top', 'bottom']} style={s.screen}>
    <View style={s.bar}>
      <Pressable accessibilityRole="button" accessibilityLabel="Back" hitSlop={12} onPress={() => (router.canGoBack() ? router.back() : router.replace('/'))}><Text style={s.back}>‹</Text></Pressable>
      <View style={s.title}>
        <Text numberOfLines={1} style={s.name}>{session?.title ?? pane}</Text>
        <Text numberOfLines={1} style={s.where}>{[session?.engine, host?.name, session?.cached ? 'saved' : session ? stateLabel[session.state] : 'not running'].filter(Boolean).join(' · ')}</Text>
      </View>
      {snap?.terminal ? <Pressable accessibilityRole="button" accessibilityLabel="Terminal display options" accessibilityState={{ expanded: displayOptions }} aria-expanded={displayOptions} hitSlop={8} style={s.display} onPress={() => setDisplayOptions(x => !x)}><Text style={s.sizeText}>Display</Text></Pressable> : <View style={s.spacer} />}
    </View>
    {session ? <CloseSessionButton session={session} onClosed={() => router.canGoBack() ? router.back() : router.replace('/')} /> : null}
    {savedDrafts.length ? <View style={s.savedDrafts}>
      <Pressable accessibilityRole="button" accessibilityLabel="View unsent agent messages" onPress={() => setShowSavedDrafts(value => !value)}><Text style={s.sizeText}>{`Unsent agent messages (${savedDrafts.length})`}</Text></Pressable>
      {showSavedDrafts ? <ScrollView style={s.savedText}>{savedDrafts.map(([key, text]) => <View key={key}>
        <Text style={s.fallbackReason}>Kept for the ended agent; this will not run as a shell command.</Text>
        <Text selectable style={s.fallbackReason}>{text}</Text>
        <Pressable accessibilityRole="button" accessibilityLabel="Dismiss saved agent message" onPress={() => app.setDraft(computer, pane, '', key.slice(draftPrefix.length))}><Text style={s.sizeText}>Dismiss</Text></Pressable>
      </View>)}</ScrollView> : null}
    </View> : null}
    <KeyboardAvoidingView style={s.body} behavior={Platform.OS === 'ios' ? 'padding' : 'height'}>
      {!snap ? <View style={s.body}><Empty>{host?.connection === 'connected' ? 'Loading…' : `Waiting for ${host?.name ?? 'the computer'} to connect.`}</Empty></View>
        : snap.terminal ? <View style={s.body}>{stale ? <Text style={s.stale}>{stale}</Text> : null}
          {fallback ? <View style={s.fallback}><Text style={s.fallbackReason}>{fallback.reason} · Using command input.</Text><Pressable accessibilityRole="button" accessibilityLabel="Retry live terminal" onPress={() => setFailure(undefined)}><Text style={s.sizeText}>Retry live typing</Text></Pressable></View> : null}
          {displayOptions ? <Pressable accessibilityRole="button" accessibilityLabel="Text size" style={s.textSize} onPress={() => setSize(x => (x + 1) % terminalSizes)}><Text style={s.sizeText}>Aa · Text size</Text></Pressable> : null}
          {!snap.cached ? <TerminalLayoutControls key={`layout:${computer}:${pane}`} computer={computer} pane={pane} viewport={viewport} collapsed={!displayOptions} /> : null}
          {!live && !snap.cached ? <TerminalHistoryButton computer={computer} pane={pane} size={size} /> : null}
          {live ? <LiveTerminal key={`live:${computer}:${pane}`} computer={computer} pane={pane} connected={host?.connection === "connected"} size={size} onViewportChange={setViewport} onClosed={liveClosed} /> : <Terminal key={`output:${computer}:${pane}`} showControls={displayOptions} ansi={fallback?.screen && (snap.terminal.receivedAt ?? 0) < fallback.at ? fallback.screen.ansi : snap.terminal.ansi} size={size} columns={fallback?.screen && (snap.terminal.receivedAt ?? 0) < fallback.at ? fallback.screen.columns : snap.terminal.columns} onViewportChange={setViewport} />}</View>
        : <View style={s.body}><Empty>Nothing to show for this session yet.</Empty></View>}
      {!live ? <Composer key={`${computer}:${pane}`} shell computerId={computer} pane={pane} placeholder="Command…" /> : null}
    </KeyboardAvoidingView>
  </SafeAreaView></MotionGate>;
}

const s = StyleSheet.create({
  screen: { flex: 1, backgroundColor: c.ground },
  bar: { flexDirection: 'row', alignItems: 'center', gap: 8, paddingHorizontal: 12, paddingBottom: 8, borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: c.line },
  back: { color: c.accent, fontSize: 32, lineHeight: 34, width: 28 },
  title: { flex: 1, alignItems: 'center', minWidth: 0 },
  name: { color: c.text, fontSize: 16, fontWeight: '700' },
  where: { color: c.muted, fontSize: 12 },
  spacer: { width: 36 },
  sizeText: { color: c.text, fontSize: 13, fontWeight: '600' },
  display: { minHeight: 40, paddingHorizontal: 8, justifyContent: 'center' },
  textSize: { minHeight: 40, paddingHorizontal: 14, justifyContent: 'center', alignSelf: 'flex-start' },
  body: { flex: 1 },
  savedDrafts: { paddingHorizontal: 14, paddingVertical: 8, backgroundColor: c.surface },
  savedText: { maxHeight: 120 },
  fallback: { padding: 8, gap: 6, backgroundColor: c.raised },
  fallbackReason: { color: c.muted, fontSize: 12 },
  stale: { color: c.warn, fontSize: 12, textAlign: 'center', paddingVertical: 4, backgroundColor: c.term },
});
