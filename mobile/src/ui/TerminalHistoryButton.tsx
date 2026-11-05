import { useRef, useState } from 'react';
import { Keyboard, Modal, Pressable, StyleSheet, Text, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { usePartyline, type TerminalHistory } from '../runtime';
import { Terminal } from './Terminal';
import { c } from './theme';

/** A frozen, read-only history view: output arriving on the computer cannot pull
 * the reader back to the bottom, and scrolling never enters shared copy mode. */
export function TerminalHistoryButton({ computer, pane, onOpen, size = 1 }: { computer: string; pane: string; onOpen?: () => void; size?: number }) {
 const app = usePartyline();
 const host = app.computers.find(h => h.id === computer);
 const [visible, setVisible] = useState(false);
 const [history, setHistory] = useState<TerminalHistory>();
 const [busy, setBusy] = useState(false);
 const [error, setError] = useState('');
 const request = useRef(0);
 const supported = !!app.readTerminalHistory && !!host?.capabilities.includes('terminal_history');
 const connected = host?.connection === 'connected';
 const load = async () => {
  const current = ++request.current;
  setBusy(true); setError('');
  try { const result = await app.readTerminalHistory!(computer, pane); if (request.current === current) setHistory(result); }
  catch (e) { if (request.current === current) setError(String(e)); }
  finally { if (request.current === current) setBusy(false); }
 };
 const hide = () => { request.current++; setBusy(false); setVisible(false); };
 return <>
  <Pressable accessibilityRole="button" accessibilityLabel="View terminal history" disabled={!supported || !connected} style={s.button} onPress={() => { onOpen?.(); Keyboard.dismiss(); setHistory(undefined); setVisible(true); void load(); }}><Text style={[s.action, (!supported || !connected) && { opacity: .4 }]}>History</Text></Pressable>
  {visible ? <Modal visible animationType="slide" onRequestClose={hide}>
   <SafeAreaView style={s.screen}>
    <View style={s.bar}><Text style={s.title}>Terminal history</Text><Pressable accessibilityRole="button" accessibilityLabel="Back to live terminal" style={s.button} onPress={hide}><Text style={s.action}>Back to terminal</Text></Pressable></View>
    <Text style={s.hint}>Read-only snapshot · swipe to scroll · up to 2,000 retained lines</Text>
    {busy ? <Text accessibilityLiveRegion="polite" style={s.hint}>Loading terminal history…</Text> : null}
    {error ? <Text accessibilityLiveRegion="polite" style={s.error}>{error}</Text> : null}
    {history?.limited ? <Text style={s.hint}>Older output exceeds the retained history or transfer limit.</Text> : null}
    {history?.alternate ? <Text style={s.hint}>This program uses an alternate screen; its output may not be retained in shell history.</Text> : null}
    {history ? <Terminal ansi={history.ansi} columns={history.columns} size={size} defaultDisplay="wrap" showControls={false} /> : <View style={{ flex: 1 }} />}
    <Pressable accessibilityRole="button" disabled={busy || !connected} style={s.button} onPress={() => { void load(); }}><Text style={[s.action, (busy || !connected) && { opacity: .4 }]}>{error ? 'Retry loading history' : 'Refresh history'}</Text></Pressable>
   </SafeAreaView>
  </Modal> : null}
 </>;
}
const s = StyleSheet.create({screen:{flex:1,backgroundColor:c.term},bar:{flexDirection:'row',alignItems:'center',paddingHorizontal:12},title:{flex:1,color:c.text,fontSize:17,fontWeight:'600'},button:{minHeight:44,justifyContent:'center',paddingHorizontal:12},action:{color:c.accent,fontSize:13},hint:{color:c.muted,fontSize:12,paddingHorizontal:12,paddingVertical:4},error:{color:c.warn,padding:12}});
