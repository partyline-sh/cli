import { useState } from 'react';
import { Alert, Keyboard, Pressable, ScrollView, StyleSheet, Text, TextInput, View } from 'react-native';
import { paneKey, usePartyline, type Delivery, type InputKey } from '../runtime';
import { AttachmentPicker } from './AttachmentPicker';
import { c, mono } from './theme';
import { useKeyboardVisible } from './useKeyboardVisible';

const keys: [string, InputKey][] = [['esc', 'Escape'], ['ctrl-c', 'C-c'], ['tab', 'Tab'], ['↑', 'Up'], ['↓', 'Down'], ['←', 'Left'], ['→', 'Right'], ['⏎', 'Enter'], ['ctrl-r', 'C-r']];

const statusText: Record<Delivery['status'], string> = {
  waiting: 'Waiting for confirmation…', confirmed: 'Confirmed', refused: 'Refused', unknown: 'Not confirmed: delivery unknown', 'not-sent': 'Not sent',
};

// The warning is the runtime's condition for retrying (retryDelivery's acknowledgeRisk): an ID only
// prevents a second run while the computer still remembers it.
const risk = 'Delivery was not confirmed. If it did arrive and the computer has since restarted, this runs it a second time.';

/** The message box, the key row (sessions only) and the last command's delivery status. */
export function Composer({ computerId, pane, placeholder }: { computerId: string; pane: string; placeholder: string }) {
  const app = usePartyline();
  const keyboardVisible = useKeyboardVisible();
  const chat = pane === 'chat';
  const draft = app.drafts[paneKey(computerId, pane)] ?? '';
  const mine = app.deliveries.filter(d => d.computerId === computerId && d.pane === pane);
  const last = mine.find(d => d.status === 'unknown') ?? mine.at(-1);
  // A rejected action is a local refusal (too long, empty, blocked behind an unknown delivery):
  // nothing was sent, and the person is told why here.
  const [error, setError] = useState('');
  const [uploading, setUploading] = useState(false);
  const run = (action: () => Promise<unknown>) => {
    setError('');
    action().catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)));
  };
  // The runtime clears the draft once the computer confirms this text, keeping any edit made since.
  const send = () => { if (draft.trim()) run(() => (chat ? app.sendChat(computerId, draft) : app.sendText(computerId, pane, draft))); };
  const retry = (d: Delivery) => {
    if (d.status !== 'unknown') { run(() => app.retryDelivery(d.id, false)); return; }
    Alert.alert('Send it again?', risk, [{ text: 'Cancel', style: 'cancel' }, { text: 'Retry', style: 'destructive', onPress: () => run(() => app.retryDelivery(d.id, true)) }]);
  };
  // Insert types the draft without pressing Return: a partial command, or the one key a prompt asks
  // for. Several lines go as one paste; a shell without bracketed paste runs each line as it lands.
  const insert = () => {
    if (!draft) return;
    const go = () => run(() => app.paste(computerId, pane, draft));
    if (!draft.includes('\n')) { go(); return; }
    Alert.alert('Insert several lines?', 'A program that has not turned on bracketed paste, such as a plain shell, runs each line as it arrives.',
      [{ text: 'Cancel', style: 'cancel' }, { text: 'Insert', onPress: go }]);
  };
  const moveOn = (d: Delivery) => Alert.alert('Send something new?', `${risk} A new command could repeat the same action.`,
    [{ text: 'Cancel', style: 'cancel' }, { text: 'Continue', style: 'destructive', onPress: () => app.acknowledgeDelivery(d.id) }]);

  return <View style={s.wrap}>
    {keyboardVisible ? <Pressable accessibilityRole="button" accessibilityLabel="Dismiss keyboard" onPress={() => Keyboard.dismiss()} style={s.done}><Text style={s.action}>Done</Text></Pressable> : null}
    {!chat ? <ScrollView horizontal showsHorizontalScrollIndicator={false} keyboardShouldPersistTaps="always" contentContainerStyle={s.keys}>
      <Pressable accessibilityRole="button" accessibilityLabel="Insert the message without pressing Return" disabled={!draft}
        style={({ pressed }) => [s.key, s.insert, !draft && { opacity: 0.4 }, pressed && { backgroundColor: c.accent }]} onPress={insert}>
        <Text style={s.keyText}>insert</Text></Pressable>
      {keys.map(([label, key]) => <Pressable key={key} accessibilityRole="button" accessibilityLabel={`Send ${label}`}
        style={({ pressed }) => [s.key, pressed && { backgroundColor: c.accent }]} onPress={() => run(() => app.sendKey(computerId, pane, key))}>
        <Text style={s.keyText}>{label}</Text></Pressable>)}
    </ScrollView> : null}
    {error ? <Text accessibilityLiveRegion="assertive" style={[s.statusText, s.error]}>{`Not sent: ${error}`}</Text> : null}
    {last && last.status !== 'confirmed' ? <View accessibilityLiveRegion="polite" style={s.status}>
      <Text style={[s.statusText, last.status === 'unknown' || last.status === 'refused' ? { color: c.warn } : null]}>
        {statusText[last.status]}{last.reason ? `: ${last.reason}` : ''}</Text>
      {last.status === 'unknown' || last.status === 'not-sent' ? <Pressable accessibilityRole="button" onPress={() => retry(last)}><Text style={s.action}>Retry</Text></Pressable> : null}
      {last.status === 'unknown' ? <Pressable accessibilityRole="button" onPress={() => moveOn(last)}><Text style={s.action}>Send a new one…</Text></Pressable> : null}
    </View> : null}
    <AttachmentPicker key={`${computerId}:${pane}`} computerId={computerId} pane={pane} onBusy={setUploading} />
    <View style={s.row}>
      <TextInput value={draft} onChangeText={t => { setError(''); app.setDraft(computerId, pane, t); }} placeholder={placeholder} placeholderTextColor={c.faint}
        multiline style={s.input} accessibilityLabel={placeholder} />
      <Pressable accessibilityRole="button" accessibilityLabel="Send" disabled={!draft.trim() || uploading} onPress={send}
        style={[s.send, (!draft.trim() || uploading) && { opacity: 0.4 }]}><Text style={s.sendText}>↑</Text></Pressable>
    </View>
  </View>;
}

const s = StyleSheet.create({
  wrap: { borderTopWidth: StyleSheet.hairlineWidth, borderTopColor: c.line, paddingBottom: 6 },
  done: { alignSelf: 'flex-end', minHeight: 36, paddingHorizontal: 16, justifyContent: 'center' },
  keys: { gap: 6, paddingHorizontal: 12, paddingTop: 8, paddingBottom: 4 },
  key: { borderWidth: StyleSheet.hairlineWidth, borderColor: c.line, backgroundColor: c.surface, borderRadius: 8, paddingHorizontal: 11, minHeight: 34, justifyContent: 'center' },
  insert: { borderColor: c.accent },
  keyText: { color: c.text, fontFamily: mono, fontSize: 12, fontWeight: '600' },
  status: { flexDirection: 'row', flexWrap: 'wrap', alignItems: 'center', gap: 12, paddingHorizontal: 14, paddingTop: 6 },
  statusText: { color: c.muted, fontSize: 13, flexShrink: 1 },
  error: { color: c.warn, paddingHorizontal: 14, paddingTop: 6 },
  action: { color: c.accent, fontSize: 13, fontWeight: '600', paddingVertical: 4 },
  row: { flexDirection: 'row', alignItems: 'flex-end', gap: 8, paddingHorizontal: 12, paddingTop: 6 },
  input: { flex: 1, minHeight: 40, maxHeight: 140, borderRadius: 20, borderWidth: StyleSheet.hairlineWidth, borderColor: c.line, backgroundColor: c.surface, color: c.text, fontSize: 15, paddingHorizontal: 14, paddingTop: 10, paddingBottom: 10 },
  send: { width: 40, height: 40, borderRadius: 20, backgroundColor: c.accent, alignItems: 'center', justifyContent: 'center' },
  sendText: { color: c.ground, fontSize: 20, fontWeight: '700' },
});
