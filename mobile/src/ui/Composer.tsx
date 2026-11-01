import { readInputFeedback } from '../runtime/input-feedback';
import { Signal } from './Signal';
import { ConnectionSignal } from './ConnectionSignal';
import { useState } from 'react';
import { Alert, Keyboard, Platform, Pressable, ScrollView, StyleSheet, Text, TextInput, View } from 'react-native';
import { usePartyline, type Delivery, type InputKey } from '../runtime';
import { inputDraftKey } from '../runtime/types';
import { AttachmentPicker } from './AttachmentPicker';
import { c, mono } from './theme';
import { useKeyboardVisible } from './useKeyboardVisible';
import { ChatInput } from './ChatInput';

const keys: [string, InputKey][] = [['esc', 'Escape'], ['ctrl-c', 'C-c'], ['tab', 'Tab'], ['↑', 'Up'], ['↓', 'Down'], ['←', 'Left'], ['→', 'Right'], ['⏎', 'Enter'], ['ctrl-r', 'C-r']];

const statusText: Record<Delivery['status'], string> = {
  waiting: 'Waiting for confirmation…', confirmed: 'Confirmed', refused: 'Refused', unknown: 'Not confirmed: delivery unknown', 'not-sent': 'Not sent',
};

// The warning is the runtime's condition for retrying (retryDelivery's acknowledgeRisk): an ID only
// prevents a second run while the computer still remembers it.
const risk = 'Delivery was not confirmed. If it did arrive and the computer has since restarted, this runs it a second time.';

/** The message box, the key row (sessions only) and the last command's delivery status. */
export function Composer({ computerId, pane, placeholder, shell = false, deliveryLabel }: { computerId: string; pane: string; placeholder: string; shell?: boolean; deliveryLabel?: (delivery: Delivery) => string | undefined }) {
  const app = usePartyline();
  const keyboardVisible = useKeyboardVisible();
  const chat = pane === 'chat';
  const target = app.sessions.find(s => s.computerId === computerId && s.pane === pane)?.agentTarget;
  const draft = app.drafts[inputDraftKey(computerId, pane, target)] ?? '';
  const mine = app.deliveries.filter(d => d.computerId === computerId && d.pane === pane && (d.target === target || d.status === 'unknown' || d.status === 'waiting'));
  const last = mine.find(d => d.status === 'unknown') ?? mine.at(-1);
  // A rejected action is a local refusal (too long, empty, blocked behind an unknown delivery):
  // nothing was sent, and the person is told why here.
  const feedback = readInputFeedback(last?.data);
  const host = app.computers.find(c => c.id === computerId);
  const session = app.sessions.find(s => s.computerId === computerId && s.pane === pane);
  const liveWorking = host?.connection === 'connected' && session?.state === 'working';
  const observedReply = last ? deliveryLabel?.(last) : undefined;
  const replyReceived = observedReply === 'Answered' || observedReply === 'Reply arriving…';
  const confirmed = observedReply ?? (chat ? 'Posted to room' : feedback?.inputDelivery === 'in_composer' ? 'Still in input box at last check'
    : feedback?.inputDelivery === 'agent_working' ? liveWorking ? 'Submitted · agent working' : 'Delivered · agent was working at last check'
    : 'Delivered to terminal');
  const [error, setError] = useState('');
  const [uploading, setUploading] = useState(false);
  const editDraft = (text: string) => target ? app.setDraft(computerId, pane, text, target) : app.setDraft(computerId, pane, text);
  const sendText = (text: string) => target ? app.sendText(computerId, pane, text, target) : app.sendText(computerId, pane, text);
  const sendKey = (key: InputKey) => target ? app.sendKey(computerId, pane, key, target) : app.sendKey(computerId, pane, key);
  const pasteText = (text: string) => target ? app.paste(computerId, pane, text, target) : app.paste(computerId, pane, text);
  const waiting = mine.some(d => d.status === 'waiting');
  const compactStatus = shell && last && (last.status === 'waiting' || last.status === 'confirmed' && feedback?.inputDelivery !== 'in_composer');
  const run = (action: () => Promise<unknown>) => {
    setError('');
    action().catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)));
  };
  // The runtime clears the draft once the computer confirms this text, keeping any edit made since.
  const send = () => { if (draft.trim()) run(() => (chat ? app.sendChat(computerId, draft) : sendText(draft))); };
  const submitCommand = (text = draft) => {
    if (uploading || waiting) {
      setError(uploading ? 'Return was blocked by the upload. Try again once it finishes.' : 'Return was blocked by pending input confirmation. Try again after confirmation.');
      return;
    }
    const go = () => run(() => text.trim() ? sendText(text) : sendKey('Enter'));
    if (!/[\r\n]/.test(text)) { go(); return; }
    Alert.alert('Run multiple lines?', 'These lines will be sent to the shell and may run as separate commands.',
      [{ text: 'Cancel', style: 'cancel' }, { text: 'Run', onPress: go }]);
  };
  const retry = (d: Delivery) => {
    if (d.status !== 'unknown') { run(() => app.retryDelivery(d.id, false)); return; }
    Alert.alert('Send it again?', risk, [{ text: 'Cancel', style: 'cancel' }, { text: 'Retry', style: 'destructive', onPress: () => run(() => app.retryDelivery(d.id, true)) }]);
  };
  // Insert types the draft without pressing Return: a partial command, or the one key a prompt asks
  // for. Several lines go as one paste; a shell without bracketed paste runs each line as it lands.
  const insert = () => {
    if (!draft) return;
    const go = () => run(() => pasteText(draft));
    if (!(shell ? /[\r\n]/.test(draft) : draft.includes('\n'))) { go(); return; }
    Alert.alert('Insert several lines?', 'A program that has not turned on bracketed paste, such as a plain shell, runs each line as it arrives.',
      [{ text: 'Cancel', style: 'cancel' }, { text: 'Insert', onPress: go }]);
  };
  const moveOn = (d: Delivery) => Alert.alert('Send something new?', `${risk} A new command could repeat the same action.`,
    [{ text: 'Cancel', style: 'cancel' }, { text: 'Continue', style: 'destructive', onPress: () => app.acknowledgeDelivery(d.id) }]);

  const attachment = <AttachmentPicker key={`${computerId}:${pane}`} computerId={computerId} pane={pane} onBusy={setUploading} compact={shell} />;
  const shellKeys = [keys[2], ...keys.slice(0, 2), ...keys.slice(3)];
  return <View style={[s.wrap, shell && s.shellWrap]}>
    {!shell && keyboardVisible ? <Pressable accessibilityRole="button" accessibilityLabel="Dismiss keyboard" onPress={() => Keyboard.dismiss()} style={s.done}><Text style={s.action}>Done</Text></Pressable> : null}
    {!chat ? <ScrollView horizontal showsHorizontalScrollIndicator={false} keyboardShouldPersistTaps="always" contentContainerStyle={s.keys}>
      <Pressable accessibilityRole="button" accessibilityLabel={shell ? 'Insert command without pressing Return' : 'Insert the message without pressing Return'} disabled={!draft || uploading || shell && waiting}
        style={({ pressed }) => [s.key, s.insert, (!draft || uploading || shell && waiting) && { opacity: 0.4 }, pressed && { backgroundColor: c.accent }]} onPress={insert}>
        <Text style={s.keyText}>insert</Text></Pressable>
      {(shell ? shellKeys : keys).map(([label, key]) => <Pressable key={key} accessibilityRole="button" accessibilityLabel={shell && key === 'Enter' ? 'Run command or send Return' : `Send ${label}`}
        style={({ pressed }) => [s.key, pressed && { backgroundColor: c.accent }]} onPress={() => shell && key === 'Enter' ? submitCommand() : run(() => sendKey(key))}>
        <Text style={s.keyText}>{label}</Text></Pressable>)}
      {shell ? attachment : null}
      {shell && keyboardVisible ? <Pressable accessibilityRole="button" accessibilityLabel="Dismiss keyboard" style={s.key} onPress={() => Keyboard.dismiss()}><Text style={s.keyText}>done</Text></Pressable> : null}
    </ScrollView> : null}
    {error ? <Text accessibilityLiveRegion="assertive" style={[s.statusText, s.error]}>{`Not sent: ${error}`}</Text> : null}
    {last && !compactStatus ? <View accessibilityLiveRegion="polite" style={s.status}>
      {last.status === 'waiting' ? <ConnectionSignal state="connecting" compact /> : <Signal mode={last.status === 'confirmed' ? replyReceived ? observedReply === 'Reply arriving…' ? 'active' : 'confirmed' : feedback?.inputDelivery === 'agent_working' && liveWorking ? 'active' : feedback?.inputDelivery === 'in_composer' ? 'waiting' : 'confirmed' : 'blocked'} colour={last.status === 'confirmed' ? c.ok : c.warn} />}
      <Text style={[s.statusText, last.status === 'confirmed' ? { color: c.ok } : null, last.status === 'unknown' || last.status === 'refused' ? { color: c.warn } : null]}>
        {last.status === 'confirmed' ? confirmed : statusText[last.status]}{last.reason ? `: ${last.reason}` : ''}</Text>
      {last.status === 'confirmed' && !replyReceived && feedback?.inputDelivery === 'in_composer' && feedback.retryInputId === last.id && host?.connection === 'connected' && host.capabilities.includes('input_feedback') ? <Pressable accessibilityRole="button" onPress={() => run(() => app.retryEnter(computerId, pane, last.id))}><Text style={s.action}>Retry Enter</Text></Pressable> : null}
      {last.status === 'unknown' || last.status === 'not-sent' ? <Pressable accessibilityRole="button" onPress={() => retry(last)}><Text style={s.action}>Retry</Text></Pressable> : null}
      {last.status === 'unknown' ? <Pressable accessibilityRole="button" onPress={() => moveOn(last)}><Text style={s.action}>Send a new one…</Text></Pressable> : null}
    </View> : null}
    {!shell ? attachment : null}
    <View style={[s.row, shell && s.commandRow]}>
      {shell ? <>
        <Text style={s.prompt} accessibilityElementsHidden importantForAccessibility="no-hide-descendants">$</Text>
        <TextInput value={draft} onChangeText={t => { setError(''); editDraft(t); }} placeholder={placeholder} placeholderTextColor={c.faint}
          accessibilityLabel="Shell command, edited on this phone" accessibilityHint="Return runs the command. Insert pastes it without Return; then Tab completes in the shell."
          multiline submitBehavior="submit" onSubmitEditing={e => submitCommand(e.nativeEvent.text)}
          onKeyPress={event => {
            // RN Web 0.21 does not implement submitBehavior. Cancel its newline
            // explicitly; native keyboards use onSubmitEditing above.
            if (Platform.OS !== 'web' || event.nativeEvent.key !== 'Enter' || (event.nativeEvent as { isComposing?: boolean }).isComposing) return;
            event.preventDefault();
            submitCommand((event.currentTarget as unknown as { value?: string }).value ?? draft);
          }}
          autoCorrect={false} autoCapitalize="none" autoComplete="off" spellCheck={false} smartInsertDelete={false}
          keyboardType={Platform.OS === 'ios' ? 'default' : 'visible-password'} returnKeyType="go" disableFullscreenUI
          style={s.command} />
        {compactStatus && (last.status === 'waiting' || !draft) ? <Text accessibilityLiveRegion="polite" accessibilityLabel={last.status === 'waiting' ? statusText.waiting : `${confirmed}${last.reason ? `: ${last.reason}` : ''}`}
          style={[s.commandState, last.status === 'confirmed' && { color: c.ok }]}>{last.status === 'waiting' ? '…' : '✓'}</Text> : null}
      </> : chat ? <ChatInput key={computerId} value={draft} onChangeText={t => { setError(''); editDraft(t); }} placeholder={placeholder} style={s.input} room={host?.connection === 'connected' ? app.chatRooms?.[computerId] : undefined} />
        : <TextInput value={draft} onChangeText={t => { setError(''); editDraft(t); }} placeholder={placeholder} placeholderTextColor={c.faint}
          multiline style={s.input} accessibilityLabel={placeholder} />}
      {!shell ? <Pressable accessibilityRole="button" accessibilityLabel="Send" disabled={!draft.trim() || uploading} onPress={send}
        style={[s.send, (!draft.trim() || uploading) && { opacity: 0.4 }]}><Text style={s.sendText}>↑</Text></Pressable> : null}
    </View>
    {shell ? <Text style={s.commandHint}>Return runs · Insert → Tab completes in shell</Text> : null}
  </View>;
}

const s = StyleSheet.create({
  wrap: { borderTopWidth: StyleSheet.hairlineWidth, borderTopColor: c.line, paddingBottom: 6 },
  shellWrap: { backgroundColor: c.term },
  prompt: { fontFamily: mono, color: c.accent, fontSize: 16, paddingVertical: 10 },
  command: { flex: 1, minHeight: 44, maxHeight: 88, fontFamily: mono, color: c.text, fontSize: 14, paddingVertical: 10, paddingHorizontal: 4 },
  commandState: { color: c.muted, fontFamily: mono, fontSize: 13, paddingVertical: 12 },
  commandRow: { alignItems: 'flex-start' },
  commandHint: { color: c.faint, fontSize: 11, paddingHorizontal: 14, paddingTop: 2 },
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
