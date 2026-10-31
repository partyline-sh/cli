import { Pressable, StyleSheet, Text, View } from 'react-native';
import type { Computer } from '../runtime/types';
import { age, c } from './theme';

export function ComputerDetails({ computer, sessions, onReconnect, onPair, onForget }: {
  computer: Computer;
  sessions: number;
  onReconnect(): void;
  onPair(): void;
  onForget(): void;
}) {
  const online = computer.connection === 'connected';
  const rePair = computer.connection === 'revoked' || !computer.relay;
  const last = age(computer.lastReceivedAt);
  return <View style={s.detail}>
    <Text style={s.text}>{online
      ? `${sessions} session${sessions === 1 ? '' : 's'} on this computer. Content is encrypted between your phone and computer.`
      : 'Sessions may still be running. This phone cannot check while disconnected.'}</Text>
    {last ? <Text style={s.muted}>{last === 'now' ? 'Last heard just now.' : `Last heard ${last} ago.`}</Text> : null}
    {!online && !rePair ? <Text style={s.muted}>Check the computer and its relay route. If you use a private VPN, check that it is connected on this phone.</Text> : null}
    {computer.error ? <Text style={s.muted}>{computer.error}</Text> : null}
    <View style={s.actions}>
      {!online ? <Pressable accessibilityRole="button" accessibilityLabel={`${rePair ? 'Pair again with' : 'Reconnect to'} ${computer.name}`} onPress={rePair ? onPair : onReconnect} style={s.action}>
        <Text style={s.actionText}>{rePair ? 'Pair again' : 'Reconnect'}</Text>
      </Pressable> : null}
      <Pressable accessibilityRole="button" accessibilityLabel={`Forget ${computer.name}`} onPress={onForget} style={s.action}><Text style={s.forget}>Forget computer</Text></Pressable>
    </View>
  </View>;
}
const s = StyleSheet.create({
  detail: { padding: 14, gap: 8, borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: c.line },
  text: { color: c.text, fontSize: 13, lineHeight: 19 },
  muted: { color: c.muted, fontSize: 12, lineHeight: 18 },
  actions: { flexDirection: 'row', flexWrap: 'wrap', gap: 10, marginTop: 2 },
  action: { paddingVertical: 11, paddingHorizontal: 13, borderRadius: 9, backgroundColor: c.raised },
  actionText: { color: c.text, fontSize: 13, fontWeight: '600' },
  forget: { color: c.accent, fontSize: 13, fontWeight: '600' },
});
