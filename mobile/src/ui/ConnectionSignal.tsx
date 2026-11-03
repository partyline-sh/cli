import { Animated, StyleSheet, Text, View } from 'react-native';
import type { ConnectionState } from '../runtime/types';
import { useSignalMotion } from './motion';
import { c, mono } from './theme';

export function ConnectionSignal({ state, compact = false }: { state: ConnectionState; compact?: boolean }) {
  const pending = state === 'connecting' || state === 'reconnecting';
  const online = state === 'connected';
  const { value, running } = useSignalMotion(pending, 1600);
  const colour = online ? c.ok : pending ? c.warn : state === 'revoked' ? c.accent : c.faint;
  const label = { connected: 'Connected', connecting: 'Connecting', reconnecting: 'Reconnecting', offline: 'Offline', revoked: 'Pair again' }[state];
  return <View style={s.row}>
    <View accessible={false} accessibilityElementsHidden importantForAccessibility="no-hide-descendants" style={[s.path, compact && s.compact]}>
      <Text style={[s.endpoint, { color: colour }]}>▪</Text><View style={[s.track, { backgroundColor: colour, opacity: online ? .6 : .2 }]} /><Text style={[s.endpoint, { color: colour }]}>{online ? '▪' : pending ? '◇' : '×'}</Text>
      {pending ? <Animated.Text style={[s.packet, { color: colour, opacity: running ? value.interpolate({ inputRange: [0, .15, .85, 1], outputRange: [0, 1, 1, 0] }) : .7, transform: [{ translateX: running ? value.interpolate({ inputRange: [0, 1], outputRange: [0, compact ? 22 : 54] }) : compact ? 11 : 27 }] }]}>■</Animated.Text> : null}
    </View>
    {!compact ? <Text style={[s.label, { color: colour }]}>{label}</Text> : null}
  </View>;
}
const s = StyleSheet.create({
  row: { flexDirection: 'row', alignItems: 'center', gap: 8 },
  path: { flexDirection: 'row', alignItems: 'center', width: 78, height: 20 },
  compact: { width: 46 }, endpoint: { fontFamily: mono, fontSize: 13 },
  track: { height: 1, flex: 1, marginHorizontal: 2 },
  packet: { position: 'absolute', left: 8, top: 3, fontFamily: mono, fontSize: 10 },
  label: { fontSize: 12, fontWeight: '600' },
});
