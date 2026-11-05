import { useEffect, useRef, useState } from 'react';
import { Alert, Keyboard, Pressable, StyleSheet, Text, View } from 'react-native';
import { WebView } from 'react-native-webview';
import type { LiveHandle, LiveEvent, LiveScreen } from '../runtime/live-terminal';
import { usePartyline } from '../runtime';
import { terminalHTML } from './live-terminal/bundle';
import type { TerminalViewport } from './terminal-layout';
import { TerminalHistoryButton } from './TerminalHistoryButton';
import { c } from './theme';

/** The terminal's own textarea owns the keyboard; no second command composer. */
export function LiveTerminal({ computer, pane, connected, size, onViewportChange, onClosed }: { computer: string; pane: string; connected: boolean; size: number; onViewportChange: (viewport: TerminalViewport) => void; onClosed: (reason: string, screen?: LiveScreen) => void }) {
  const app = usePartyline();
  const openLiveTerminal = app.openLiveTerminal;
  const web = useRef<WebView<unknown>>(null);
  const handle = useRef<LiveHandle | undefined>(undefined);
  const lastScreen = useRef<LiveScreen | undefined>(undefined);
  const [loaded, setLoaded] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const [paused, setPaused] = useState('');
  const [status, setStatus] = useState('Connecting terminal…');
  const post = (message: unknown) => web.current?.injectJavaScript(`window.liveReceive && window.liveReceive(${JSON.stringify(message).replace(/</g, '\\u003c')});true;`);
  useEffect(() => {
    if (!loaded || !connected || !openLiveTerminal) return;
    let alive = true;
    handle.current = openLiveTerminal(computer, pane, (event: LiveEvent) => {
      if (!alive) return;
      post(event);
      if (event.type === 'ready') { setStatus('Loading terminal…'); setPaused(''); }
      if (event.type === 'screen') { lastScreen.current = event.screen; setStatus(''); setPaused(event.screen.typing ? '' : `${event.screen.foreground} is running; only Ctrl-C and Ctrl-Z are sent.`); }
      if (event.type === 'closed') { setStatus(event.reason); onClosed(event.reason, lastScreen.current); }
    });
    return () => { alive = false; handle.current?.close(); handle.current = undefined; post({ type: 'closed' }); };
  }, [loaded, connected, computer, pane, attempt, openLiveTerminal, onClosed]);
  useEffect(() => { if (loaded) post({ type: 'size', font: [10, 11.5, 13, 15][size] ?? 13 }); }, [loaded, size]);
  const paste = (text: string) => {
    if (!text) return;
    const normalized = text.replace(/\r\n?/g, '\n');
    if (new TextEncoder().encode(normalized).length > 2048 || /[\x00-\x08\x0b-\x1f\x7f-\x9f]/.test(normalized)) { setStatus('Paste up to 2 KB of plain text at a time.'); return; }
    if (normalized.includes('\n')) Alert.alert('Paste multiple lines?', 'This may run commands immediately in the shell.', [{ text: 'Cancel', style: 'cancel' }, { text: 'Paste', onPress: () => post({ type: 'paste', text: normalized }) }]);
    else post({ type: 'paste', text: normalized });
  };
  return <View style={s.body}>
    {status ? <View style={s.status}><Text style={s.message}>{status}</Text><Pressable accessibilityRole="button" onPress={() => { setStatus('Connecting terminal…'); setAttempt(x => x + 1); }}><Text style={s.action}>Reopen</Text></Pressable></View> : null}
    {paused ? <View style={s.status}><Text style={s.message}>{paused}</Text></View> : null}
    <View style={s.history}><TerminalHistoryButton computer={computer} pane={pane} size={size} onOpen={() => { post({ type: 'blur' }); Keyboard.dismiss(); }} /><Pressable accessibilityRole="button" accessibilityLabel="Hide terminal keyboard" style={s.hide} onPress={() => { post({ type: 'blur' }); Keyboard.dismiss(); }}><Text style={s.action}>Hide keyboard</Text></Pressable></View>
    <WebView<unknown> ref={web} source={{ html: terminalHTML }} style={s.web} originWhitelist={['about:*']} onShouldStartLoadWithRequest={request => request.url === 'about:blank'}
      javaScriptEnabled domStorageEnabled={false} allowFileAccess={false} allowFileAccessFromFileURLs={false} allowUniversalAccessFromFileURLs={false}
      mixedContentMode="never" keyboardDisplayRequiresUserAction={false} hideKeyboardAccessoryView automaticallyAdjustContentInsets={false} bounces={false}
      onContentProcessDidTerminate={() => { handle.current?.close(); setLoaded(false); web.current?.reload(); }}
      onMessage={event => {
        try {
          const message = JSON.parse(event.nativeEvent.data);
          if (message.type === 'loaded') setLoaded(true);
          else if (message.type === 'input' && typeof message.data === 'string' && ['text', 'key', 'paste'].includes(message.kind) && Number.isSafeInteger(message.seq)) handle.current?.send(message.seq, message.kind, message.data);
          else if (message.type === 'paste' && typeof message.text === 'string') paste(message.text);
          else if (message.type === 'notice') setStatus(String(message.text));
          else if (message.type === 'viewport' && Number.isInteger(message.columns) && Number.isInteger(message.rows)) onViewportChange({ columns: message.columns, rows: message.rows });
        } catch (error) { setStatus(String(error)); }
      }} />
    <View style={s.keys}>{[['Esc', 'Escape'], ['Ctrl-C', 'C-c'], ['Ctrl-Z', 'C-z'], ['Tab', 'Tab'], ['↑', 'Up'], ['↓', 'Down'], ['←', 'Left'], ['→', 'Right']].map(([label, key]) => <Pressable key={key} accessibilityRole="button" accessibilityLabel={label} style={s.key} onPress={() => post({ type: 'key', key })}><Text style={s.keyText}>{label}</Text></Pressable>)}
      <Pressable accessibilityRole="button" accessibilityLabel="Show terminal keyboard" style={s.key} onPress={() => post({ type: 'focus' })}><Text style={s.keyText}>Type</Text></Pressable></View>
  </View>;
}
const s = StyleSheet.create({ body: { flex: 1 }, history: { flexDirection: 'row', justifyContent: 'space-between' }, hide: { minHeight: 44, paddingHorizontal: 12, justifyContent: 'center' }, web: { flex: 1, backgroundColor: c.term }, status: { padding: 8, flexDirection: 'row', gap: 8, backgroundColor: c.raised }, message: { flex: 1, color: c.muted, fontSize: 12 }, action: { color: c.accent, fontSize: 12 }, keys: { flexDirection: 'row', paddingVertical: 6, gap: 2, backgroundColor: c.term }, key: { flex: 1, minHeight: 40, alignItems: 'center', justifyContent: 'center' }, keyText: { color: c.text, fontSize: 12 } });
