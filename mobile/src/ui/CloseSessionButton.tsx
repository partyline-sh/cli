import { useEffect, useState } from 'react';
import { Alert, Pressable, Text, View } from 'react-native';
import { usePartyline, type Session } from '../runtime';
import { ControlDelivery } from './ControlDelivery';
import { c } from './theme';

/** Closing stops the process; it never deletes the saved conversation. */
export function CloseSessionButton({ session, onClosed }: { session: Session; onClosed?: () => void }) {
 const app = usePartyline();
 const [error, setError] = useState('');
 const [sent, setSent] = useState<string>();
 const host = app.computers.find(k => k.id === session.computerId);
 const delivery = [...app.deliveries].reverse().find(d => d.computerId === session.computerId && d.pane === `close:${session.pane}`);
 useEffect(() => {
  if (!onClosed || !sent || delivery?.id !== sent || delivery.status !== 'confirmed') return;
  const timer = setTimeout(onClosed, 0); return () => clearTimeout(timer);
 }, [delivery, sent, onClosed]);
 const terminal = !!session.terminalTarget && !!app.closeSharedTerminal && !!host?.capabilities.includes('terminal_controls');
 if (!host || session.cached) return null;
 if (!terminal && (!session.key || session.detected || !app.closeSavedSession || !host?.capabilities.includes('session_manager'))) return null;
 const label = terminal ? 'Close terminal' : 'Close session';
 const blocked = host.connection !== 'connected' || delivery?.status === 'waiting' || delivery?.status === 'unknown';
 return <View>
  <Pressable accessibilityRole="button" accessibilityLabel={`${label}: ${session.title}`} disabled={blocked} style={{ minHeight: 44, justifyContent: 'center', paddingHorizontal: 12, opacity: blocked ? .4 : 1 }} onPress={event => { event.stopPropagation(); Alert.alert(`${label}?`, terminal ? `This ends ${session.title} and any work in progress on ${host.name}. Detached background jobs may continue. This cannot be undone.` : `This stops ${session.title} and any work in progress on ${host.name}. Its saved conversation stays in All sessions so you can reopen it.`, [
   { text: 'Cancel', style: 'cancel' },
   { text: label, style: 'destructive', onPress: () => { setError(''); void (terminal ? app.closeSharedTerminal!(session.computerId, session.pane, session.terminalTarget!) : app.closeSavedSession!(session.computerId, session.pane, session.key!)).then(result => setSent(result.id)).catch(e => setError(String(e))); } },
  ]); }}><Text style={{ color: c.accent, fontSize: 13 }}>{label}</Text></Pressable>
  {error ? <Text style={{ color: c.warn }}>{error}</Text> : null}
  {delivery && delivery.status !== 'confirmed' ? <ControlDelivery delivery={delivery} /> : null}
 </View>;
}
