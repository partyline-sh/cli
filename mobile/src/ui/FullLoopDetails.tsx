import { useEffect, useState } from 'react';
import { Linking, Pressable, StyleSheet, Text, TextInput, View } from 'react-native';
import { usePartyline, type LoopAction, type LoopConfirmation, type LoopDetail } from '../runtime';
import { ControlDelivery } from './ControlDelivery';
import { c } from './theme';

const labels: Record<LoopAction, string> = { approve: 'Approve brief', reject: 'Request changes', fix: 'Require fix', dismiss: 'Dismiss finding', stop: 'Stop loop / batch', delete: 'Delete loop / batch', resume: 'Resume review', reverse: 'Reverse arbiter dismissal' };

export function FullLoopDetails({ computer, id, receivedAt }: { computer: string; id: string; receivedAt?: number }) {
  const app = usePartyline();
  const host = app.computers.find(c => c.id === computer);
  const supported = host?.capabilities.includes('loop_controls');
  const connected = host?.connection === 'connected';
  const [detail, setDetail] = useState<LoopDetail>();
  const [confirmation, setConfirmation] = useState<LoopConfirmation>();
  const [code, setCode] = useState('');
  const [note, setNote] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [refresh, setRefresh] = useState(0);
  const delivery = [...app.deliveries].reverse().find(d => d.computerId === computer && d.pane === `loop:${id}`);
  const blocked = busy || !connected || delivery?.status === 'waiting' || delivery?.status === 'unknown';
  const read = app.readLoop;
  useEffect(() => {
    let cancelled = false;
    if (!connected || !supported) return;
    void read(computer, id).then(value => { if (!cancelled) { setDetail(value); setError(''); } }).catch(e => { if (!cancelled) setError(String(e)); });
    return () => { cancelled = true; };
  }, [computer, id, receivedAt, connected, supported, read, refresh, delivery?.status]);
  const prepare = async (action: LoopAction) => {
    setBusy(true); setError(''); setCode(''); setNote('');
    try {
      const value = await app.prepareLoopAction(computer, id, action);
      setDetail(value.detail); setConfirmation(value);
    } catch (e) { setError(String(e)); }
    finally { setBusy(false); }
  };
  const confirm = async () => {
    if (!confirmation) return;
    setBusy(true); setError('');
    try { await app.confirmLoopAction(computer, id, confirmation, code, note); setConfirmation(undefined); setCode(''); }
    catch (e) { setError(String(e)); }
    finally { setBusy(false); }
  };
  if (!supported) return <Text style={s.muted}>Update Partyline on this computer for the full brief, history and controls.</Text>;
  const shown = confirmation?.detail ?? detail;
  const pr = shown?.pr;
  // Only open an actual HTTPS pull-request link, never a command or arbitrary URL
  // supplied by a session's title. Numeric PRs remain visible in the board summary.
  const prURL = pr && /^https:\/\/[^\s/?#]+\/[^\s]+\/pull\/\d+\/?$/.test(pr) ? pr : undefined;
  return <View style={s.panel}>
    <View style={s.bar}><Text style={s.heading}>Full loop</Text><Pressable accessibilityRole="button" disabled={!connected || busy} onPress={() => setRefresh(n => n + 1)}><Text style={s.action}>Refresh</Text></Pressable></View>
    {error ? <Text accessibilityLiveRegion="assertive" style={s.warning}>{error}</Text> : null}
    {!shown ? <Text style={s.muted}>Loading full brief and history…</Text> : <>
      <Text style={s.muted}>{`Revision ${shown.revision} · ${shown.state}`}</Text>
      <Text style={s.heading}>Brief</Text><Text selectable style={s.body}>{shown.brief || 'No brief yet.'}</Text>
      <Text style={s.heading}>History, checks and findings</Text><Text selectable style={s.body}>{shown.history}</Text>
      {shown.dispute.id ? <><Text style={s.heading}>Disputed finding {shown.dispute.id}</Text><Text selectable style={s.body}>{shown.dispute.claim}{'\n\n'}{shown.dispute.dispute}</Text></> : null}
      {prURL ? <Pressable accessibilityRole="link" onPress={() => { void Linking.openURL(prURL).catch(e => setError(String(e))); }}><Text style={s.action}>Open pull request ↗</Text></Pressable> : null}
      {confirmation ? <View style={s.confirm}>
        <Text style={s.heading}>{labels[confirmation.action]}</Text>
        <Text style={s.body}>{confirmation.action === 'delete' ? `Permanently removes ${shown.deleteTarget}, including every step and its records. Running contractors will stop.` : confirmation.action === 'stop' ? 'Stops the loop and its current batch. Deferred findings may still need a ruling.' : `Confirm ${labels[confirmation.action].toLowerCase()} for loop ${id}, revision ${confirmation.revision}.`}</Text>
        <Text style={s.body}>Read the brief and findings above. Type {confirmation.code} to confirm. This code expires in five minutes.</Text>
        {confirmation.action === 'reject' ? <TextInput accessibilityLabel="Reason for requesting changes" placeholder="What needs to change?" placeholderTextColor={c.muted} multiline value={note} onChangeText={setNote} maxLength={8000} style={s.input} /> : null}
        <TextInput accessibilityLabel="Four digit confirmation code" value={code} onChangeText={setCode} keyboardType="number-pad" maxLength={4} autoCorrect={false} style={s.input} />
        <Pressable accessibilityRole="button" disabled={blocked || code !== confirmation.code || confirmation.action === 'reject' && !note.trim()} onPress={() => { void confirm(); }}><Text style={[s.action, (blocked || code !== confirmation.code || confirmation.action === 'reject' && !note.trim()) && s.muted]}>Confirm {labels[confirmation.action].toLowerCase()}</Text></Pressable>
        <Pressable accessibilityRole="button" disabled={busy} onPress={() => { setConfirmation(undefined); setCode(''); }}><Text style={s.action}>Cancel</Text></Pressable>
      </View> : <View style={s.actions}>{shown.actions.map(action => <Pressable key={action} accessibilityRole="button" disabled={blocked} style={s.button} onPress={() => { void prepare(action); }}><Text style={blocked ? s.muted : s.action}>{labels[action]}</Text></Pressable>)}</View>}
    </>}
    <ControlDelivery delivery={delivery} />
  </View>;
}
const s = StyleSheet.create({
  panel: { padding: 14, gap: 14, backgroundColor: c.surface, borderRadius: 14 },
  bar: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center' },
  heading: { color: c.text, fontSize: 16, fontWeight: '700' },
  body: { color: c.text, fontSize: 14, lineHeight: 22 },
  muted: { color: c.muted, fontSize: 13, lineHeight: 19 },
  warning: { color: c.warn, fontSize: 14 },
  action: { color: c.accent, fontSize: 15 },
  actions: { gap: 10 }, button: { paddingVertical: 10 },
  confirm: { gap: 16, padding: 14, backgroundColor: c.raised, borderRadius: 10 },
  input: { padding: 12, borderColor: c.line, borderWidth: 1, borderRadius: 8, color: c.text, fontSize: 17 },
});
