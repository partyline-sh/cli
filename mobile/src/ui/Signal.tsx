import { Animated, StyleSheet, Text, View } from 'react-native';
import { useSignalMotion } from './motion';
import { c, mono } from './theme';

export type SignalMode = 'active' | 'waiting' | 'idle' | 'confirmed' | 'blocked' | 'offline';
export function Signal({ mode, colour = c.ok }: { mode: SignalMode; colour?: string }) {
  const { value, running } = useSignalMotion(mode === 'active');
  const mark = mode === 'waiting' ? '◆' : mode === 'confirmed' ? '✓' : mode === 'blocked' ? '!' : mode === 'offline' ? '×' : '·';
  return <View pointerEvents="none" accessible={false} accessibilityElementsHidden importantForAccessibility="no-hide-descendants" style={s.signal}>
    <Text style={[s.bracket, { color: colour }]}>[</Text>
    {mode === 'active' ? <View style={s.meter}>{[0, 1, 2, 3, 4].map(i => {
      const levels = [0, 1, 2, 3, 4].map(step => .25 + ((step + i) % 4) / 4);
      levels[4] = levels[0];
      return <Animated.View key={i} style={[s.bar, { backgroundColor: colour, opacity: running ? value.interpolate({ inputRange: [0, .25, .5, .75, 1], outputRange: levels }) : .7, transform: [{ scaleY: running ? value.interpolate({ inputRange: [0, .25, .5, .75, 1], outputRange: levels }) : .65 }] }]} />;
    })}</View> : <Text style={[s.mark, { color: colour }]}>{mark}</Text>}
    <Text style={[s.bracket, { color: colour }]}>]</Text>
  </View>;
}
const s = StyleSheet.create({
  signal: { height: 18, flexDirection: 'row', alignItems: 'center', gap: 2 },
  bracket: { fontFamily: mono, fontSize: 13, opacity: .55 },
  meter: { width: 23, flexDirection: 'row', alignItems: 'center', justifyContent: 'center', gap: 2 },
  bar: { width: 3, height: 12, borderRadius: 1 },
  mark: { width: 23, textAlign: 'center', fontFamily: mono, fontSize: 12 },
});
