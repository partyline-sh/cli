import { MotionGate } from './motion';
import { router, useFocusEffect } from 'expo-router';
import { useCallback, useState } from 'react';
import { KeyboardAvoidingView, Platform, Pressable, StyleSheet, Text, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { paneKey, usePartyline } from '../runtime';
import type { LiveScreen } from '../runtime/live-terminal';
import { LiveTerminal } from './LiveTerminal';
import { Composer } from './Composer';
import { Messages } from './Messages';
import { Terminal, terminalSizes } from './Terminal';
import { Empty } from './parts';
import { paneFromRoute } from './route';
import { c, clock } from './theme';
import { TerminalLayoutControls } from './TerminalLayoutControls';
import { isShellSession, type TerminalViewport } from './terminal-layout';

const stateLabel = { waiting: 'waiting on you', working: 'working', idle: 'idle' } as const;

/** One session: its conversation where a transcript exists, else the terminal; the message box below. */
export function SessionScreen({ computer, pane: routePane }: { computer: string; pane: string }) {
  const pane = paneFromRoute(routePane);
  const app = usePartyline();
  const [size, setSize] = useState(1);
  const [viewport, setViewport] = useState<TerminalViewport>();
  const [displayOptions, setDisplayOptions] = useState(false);
  const [failure, setFailure] = useState<{ key: string; reason: string; screen?: LiveScreen; at: number }>();
  const sessionKey = `${computer}:${pane}`;
  const fallback = failure?.key === sessionKey ? failure : undefined;
  const liveClosed = useCallback((reason: string, screen?: LiveScreen) => setFailure({ key: sessionKey, reason, screen, at: Date.now() }), [sessionKey]);
  useFocusEffect(useCallback(() => {
    if (app.loading) return;
    app.selectSession({ computerId: computer, pane });
    return () => app.selectSession(undefined);
  }, [computer, pane, app.loading])); // eslint-disable-line react-hooks/exhaustive-deps
  const session = app.sessions.find(s => s.computerId === computer && s.pane === pane);
  const host = app.computers.find(k => k.id === computer);
  const snap = app.panes[paneKey(computer, pane)];
  const hasTranscript = !!snap?.transcriptSupported && !!snap.transcript;
  const view = hasTranscript ? app.sessionView : 'terminal';
  const asOf = view === 'messages' ? snap?.transcript?.receivedAt : snap?.terminal?.receivedAt;
  const stale = snap?.stale && asOf ? `As of ${clock(asOf)}; reconnecting` : undefined;
  const engine = session?.engine ?? 'the session';
  const shell = !!session && isShellSession(session.engine);
  const live = !fallback && shell && Platform.OS !== "web" && !!host?.capabilities.includes("live_terminal") && !!app.openLiveTerminal;

  return <MotionGate active={host?.connection === 'connected' && !snap?.stale}><SafeAreaView edges={['top', 'bottom']} style={s.screen}>
    <View style={s.bar}>
      <Pressable accessibilityRole="button" accessibilityLabel="Back" hitSlop={12} onPress={() => (router.canGoBack() ? router.back() : router.replace('/'))}><Text style={s.back}>‹</Text></Pressable>
      <View style={s.title}>
        <Text numberOfLines={1} style={s.name}>{session?.title ?? pane}</Text>
        <Text numberOfLines={1} style={s.where}>{[session?.engine, host?.name, session ? stateLabel[session.state] : 'not running'].filter(Boolean).join(' · ')}</Text>
      </View>
      {view === 'terminal' && snap?.terminal ? shell ? <Pressable accessibilityRole="button" accessibilityLabel="Terminal display options" accessibilityState={{ expanded: displayOptions }} aria-expanded={displayOptions} hitSlop={8} style={s.display} onPress={() => setDisplayOptions(x => !x)}><Text style={s.sizeText}>Display</Text></Pressable>
        : <Pressable accessibilityRole="button" accessibilityLabel="Text size" hitSlop={8} style={s.size} onPress={() => setSize(x => (x + 1) % terminalSizes)}><Text style={s.sizeText}>Aa</Text></Pressable> : <View style={s.spacer} />}
    </View>
    {hasTranscript ? <View accessibilityRole="tablist" style={s.toggle}>
      {(['messages', 'terminal'] as const).map(v => <Pressable key={v} accessibilityRole="tab" accessibilityState={{ selected: view === v }}
        onPress={() => app.selectView(v)} style={[s.seg, view === v && s.segOn]}>
        <Text style={[s.segText, view === v && { color: c.text }]}>{v === 'messages' ? 'Messages' : 'Terminal'}</Text></Pressable>)}
    </View> : null}
    <KeyboardAvoidingView style={s.body} behavior={Platform.OS === 'ios' ? 'padding' : 'height'}>
      {!snap ? <View style={s.body}><Empty>{host?.connection === 'connected' ? 'Loading…' : `Waiting for ${host?.name ?? 'the computer'} to connect.`}</Empty></View>
        : view === 'messages' && snap.transcript ? <Messages entries={snap.transcript.entries} stale={stale} />
        : snap.terminal ? <View style={s.body}>{stale ? <Text style={s.stale}>{stale}</Text> : null}
          {fallback ? <View style={s.fallback}><Text style={s.fallbackReason}>{fallback.reason} · Using command input.</Text><Pressable accessibilityRole="button" accessibilityLabel="Retry live terminal" onPress={() => setFailure(undefined)}><Text style={s.sizeText}>Retry live typing</Text></Pressable></View> : null}
          {shell && displayOptions ? <Pressable accessibilityRole="button" accessibilityLabel="Text size" style={s.textSize} onPress={() => setSize(x => (x + 1) % terminalSizes)}><Text style={s.sizeText}>Aa · Text size</Text></Pressable> : null}
          <TerminalLayoutControls key={`layout:${computer}:${pane}`} computer={computer} pane={pane} viewport={viewport} collapsed={shell && !displayOptions} />
          {live ? <LiveTerminal key={`live:${computer}:${pane}`} computer={computer} pane={pane} connected={host?.connection === "connected"} size={size} onViewportChange={setViewport} onClosed={liveClosed} /> : <Terminal key={`output:${computer}:${pane}`} showControls={!shell || displayOptions} ansi={fallback?.screen && (snap.terminal.receivedAt ?? 0) < fallback.at ? fallback.screen.ansi : snap.terminal.ansi} size={size} columns={fallback?.screen && (snap.terminal.receivedAt ?? 0) < fallback.at ? fallback.screen.columns : snap.terminal.columns} onViewportChange={setViewport} />}</View>
        : <View style={s.body}><Empty>Nothing to show for this session yet.</Empty></View>}
      {!live ? <Composer key={`${computer}:${pane}:${shell}`} shell={shell} computerId={computer} pane={pane} placeholder={shell ? 'Command…' : `Message ${engine}…`} /> : null}
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
  size: { width: 36, height: 36, borderRadius: 18, backgroundColor: c.raised, alignItems: 'center', justifyContent: 'center' },
  sizeText: { color: c.text, fontSize: 13, fontWeight: '600' },
  display: { minHeight: 40, paddingHorizontal: 8, justifyContent: 'center' },
  textSize: { minHeight: 40, paddingHorizontal: 14, justifyContent: 'center', alignSelf: 'flex-start' },
  toggle: { flexDirection: 'row', alignSelf: 'center', backgroundColor: c.raised, borderRadius: 9, padding: 2, marginTop: 8 },
  seg: { paddingHorizontal: 14, paddingVertical: 6, borderRadius: 7 },
  segOn: { backgroundColor: c.surface },
  segText: { color: c.muted, fontSize: 12, fontWeight: '600' },
  body: { flex: 1 },
  fallback: { padding: 8, gap: 6, backgroundColor: c.raised },
  fallbackReason: { color: c.muted, fontSize: 12 },
  stale: { color: c.warn, fontSize: 12, textAlign: 'center', paddingVertical: 4, backgroundColor: c.term },
});
