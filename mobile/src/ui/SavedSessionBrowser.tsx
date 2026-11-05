import { useEffect, useRef, useState } from 'react';
import { KeyboardAvoidingView, Modal, Platform, Pressable, ScrollView, StyleSheet, Text, TextInput, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { usePartyline, type Computer, type SavedSession, type SavedSessionPage } from '../runtime';
import { router } from 'expo-router';
import { paneToRoute } from './route';
import { CloseSessionButton } from './CloseSessionButton';
import { ControlDelivery } from './ControlDelivery';
import { c } from './theme';

const openSession = ({ computerId, pane }: { computerId: string; pane: string }) => router.push({ pathname: '/session/[computer]/[pane]', params: { computer: computerId, pane: paneToRoute(pane) } });

export function SavedSessionBrowser({ computer }: { computer: Computer }) {
 const app = usePartyline();
 const [visible, setVisible] = useState(false);
 const [query, setQuery] = useState('');
 const [page, setPage] = useState<SavedSessionPage>();
 const [busy, setBusy] = useState(false);
 const [error, setError] = useState('');
 const [revision, setRevision] = useState(0);
 const [request, setRequest] = useState<string>();
 const generation = useRef(0);
 const read = useRef(app.listSavedSessions);
 useEffect(() => { read.current = app.listSavedSessions; }, [app.listSavedSessions]);
 const connected = computer.connection === 'connected';
 const supported = !!app.listSavedSessions && !!app.openSavedSession && computer.capabilities.includes('session_manager');
 const delivery = app.deliveries.find(d => d.computerId === computer.id && d.id === request);
 const opening = delivery?.status === 'waiting' || delivery?.status === 'unknown';
 const load = async (offset: number, version: number) => {
  setBusy(true); setError('');
  try {
   const next = await read.current!(computer.id, query, offset);
   if (version !== generation.current) return;
   setPage(prior => offset && prior ? { ...next, sessions: [...prior.sessions, ...next.sessions] } : next);
  } catch (e) { if (version === generation.current) setError(String(e)); }
  finally { if (version === generation.current) setBusy(false); }
 };
 useEffect(() => {
  const version = ++generation.current;
  if (!visible || !connected || !supported) return;
  const timer = setTimeout(() => { setPage(undefined); void load(0, version); }, 250);
  return () => { clearTimeout(timer); generation.current = version + 1; };
 }, [visible, connected, supported, query, revision]); // eslint-disable-line react-hooks/exhaustive-deps
 useEffect(() => {
  if (!visible || delivery?.status !== 'confirmed') return;
  const pane = (delivery.data as { pane?: unknown } | undefined)?.pane;
  if (typeof pane !== 'string' || !/^%\d+$/.test(pane)) return;
  const timer = setTimeout(() => { setRequest(undefined); setVisible(false); openSession({ computerId: computer.id, pane }); }, 0);
  return () => clearTimeout(timer);
 }, [visible, delivery, computer.id]);
 const open = async (row: SavedSession) => {
  if (opening || busy) return;
  if (row.pane) { setVisible(false); openSession({ computerId: computer.id, pane: row.pane }); return; }
  setBusy(true); setError('');
  try { const sent = await app.openSavedSession!(computer.id, row.engine, row.key); setRequest(sent.id); }
  catch (e) { setError(String(e)); }
  finally { setBusy(false); }
 };
 return <View style={s.entry}>
  <Pressable accessibilityRole="button" accessibilityLabel={`All sessions on ${computer.name}`} disabled={!connected || !supported} onPress={() => { setRequest(undefined); setBusy(true); setVisible(true); }} style={s.button}><Text style={{ color: connected && supported ? c.accent : c.faint }}>All sessions</Text></Pressable>
  {!supported ? <Text style={s.muted}>Update Partyline on this computer to browse saved conversations.</Text> : null}
  <Modal visible={visible} animationType="slide" onRequestClose={() => setVisible(false)}>
   <SafeAreaView style={s.screen}>
    <KeyboardAvoidingView style={s.screen} behavior={Platform.OS === 'ios' ? 'padding' : 'height'}>
     <View style={s.header}><View style={{ flex: 1 }}><Text style={s.title}>All sessions</Text><Text style={s.muted}>{computer.name} · saved conversations</Text></View><Pressable accessibilityRole="button" onPress={() => setVisible(false)} style={s.button}><Text style={s.link}>Hide</Text></Pressable></View>
     <TextInput accessibilityLabel="Search saved sessions" placeholder="Search name, project or engine…" placeholderTextColor={c.faint} value={query} onChangeText={value => { setQuery(value); setBusy(true); }} style={s.search} autoCapitalize="none" />
     {!connected ? <Text accessibilityLiveRegion="polite" style={s.notice}>Waiting for {computer.name} to reconnect…</Text> : null}
     {error ? <Text accessibilityLiveRegion="polite" style={s.notice}>{error}</Text> : null}
     {busy ? <Text accessibilityLiveRegion="polite" style={s.notice}>Loading sessions…</Text> : null}
     <ControlDelivery delivery={delivery} />
     <ScrollView keyboardShouldPersistTaps="handled" style={{ flex: 1 }}>
      {page?.sessions.map(row => {
       const active = app.sessions.find(x => x.computerId === computer.id && x.key === row.key);
       return <View key={`${row.engine}:${row.key}`} style={s.row}>
        <Pressable accessibilityRole="button" accessibilityLabel={`Open ${row.title}`} disabled={!!row.unavailable || !connected || opening || busy} onPress={() => { void open(row); }} style={s.button}>
         <Text numberOfLines={2} style={s.name}>{row.title}</Text><Text style={s.muted}>{row.engine} · {active || row.pane ? 'Open' : 'Saved'}</Text><Text numberOfLines={1} style={s.muted}>{row.project}</Text>
         {row.unavailable ? <Text style={s.notice}>{row.unavailable}</Text> : null}
        </Pressable>
        {active ? <CloseSessionButton session={active} /> : null}
       </View>;
      })}
      {page && !page.total ? <Text style={s.notice}>No saved sessions match.</Text> : null}
      {page?.nextOffset !== undefined ? <Pressable accessibilityRole="button" disabled={busy || !connected} style={s.button} onPress={() => { void load(page.nextOffset!, generation.current); }}><Text style={s.link}>Load more ({page.sessions.length} of {page.total})</Text></Pressable> : null}
     </ScrollView>
     <Pressable accessibilityRole="button" disabled={busy || !connected} style={s.button} onPress={() => { setBusy(true); setRevision(n => n + 1); }}><Text style={s.link}>Refresh list</Text></Pressable>
    </KeyboardAvoidingView>
   </SafeAreaView>
  </Modal>
 </View>;
}
const s = StyleSheet.create({
 entry: { marginTop: 10 }, screen: { flex: 1, backgroundColor: c.ground }, header: { flexDirection: 'row', alignItems: 'center', padding: 14 },
 title: { color: c.text, fontSize: 24, fontWeight: '700' }, name: { color: c.text, fontSize: 16 }, muted: { color: c.muted, fontSize: 12 },
 link: { color: c.accent }, button: { minHeight: 44, justifyContent: 'center', padding: 12, gap: 4 },
 search: { minHeight: 44, marginHorizontal: 14, padding: 12, backgroundColor: c.surface, borderRadius: 10, color: c.text },
 notice: { color: c.muted, padding: 12 }, row: { borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: c.line },
});
