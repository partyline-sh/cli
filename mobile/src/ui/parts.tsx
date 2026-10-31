import { useEffect, useState, type ReactNode } from 'react';
import { AccessibilityInfo, ActivityIndicator, Animated, Easing, Pressable, StyleSheet, Text, View } from 'react-native';
import type { SessionState } from '../runtime';
import { c, engineColour, mono } from './theme';

/** The engine's initial on its colour. */
export function Avatar({ engine, size = 38, label }: { engine: string; size?: number; label?: string }) {
  return <View style={[s.avatar, { width: size, height: size, borderRadius: size > 32 ? 10 : 8, backgroundColor: engineColour(engine) }]}>
    <Text style={[s.avatarText, { fontSize: size > 32 ? 15 : 12 }]}>{(label ?? engine).slice(0, 1).toUpperCase()}</Text>
  </View>;
}

/** A small spinning ring; still when the person asked for reduced motion. */
export function Spinner({ colour = c.ok }: { colour?: string }) {
  const [turn] = useState(() => new Animated.Value(0));
  useEffect(() => {
    let loop: Animated.CompositeAnimation | undefined;
    AccessibilityInfo.isReduceMotionEnabled().then(reduced => {
      if (reduced) return;
      loop = Animated.loop(Animated.timing(turn, { toValue: 1, duration: 800, easing: Easing.linear, useNativeDriver: true }));
      loop.start();
    }).catch(() => {});
    return () => loop?.stop();
  }, [turn]);
  const rotate = turn.interpolate({ inputRange: [0, 1], outputRange: ['0deg', '360deg'] });
  return <Animated.View style={[s.spin, { borderColor: colour, borderRightColor: 'transparent', transform: [{ rotate }] }]} />;
}

export function StatePill({ state }: { state: SessionState }) {
  if (state === 'waiting') return <View style={[s.pill, { backgroundColor: 'rgba(255,94,138,0.2)' }]}><Text style={[s.pillText, { color: c.accent }]}>Waiting</Text></View>;
  if (state === 'working') return <View style={[s.pill, s.row, { backgroundColor: 'rgba(74,222,128,0.16)' }]}><Spinner /><Text style={[s.pillText, { color: c.ok }]}>Working</Text></View>;
  return <View style={[s.pill, { backgroundColor: c.raised }]}><Text style={[s.pillText, { color: c.muted }]}>Idle</Text></View>;
}

/** A section label over a list: "WAITING ON YOU", "MACBOOK PRO   ● connected". */
export function Group({ title, right, rightColour = c.ok }: { title: string; right?: string; rightColour?: string }) {
  return <View style={s.group}>
    <Text style={s.groupText}>{title.toUpperCase()}</Text>
    {right ? <Text style={[s.groupRight, { color: rightColour }]}>{right}</Text> : null}
  </View>;
}

export function List({ children }: { children: ReactNode }) { return <View style={s.list}>{children}</View>; }

/** One row in a list; `last` drops the divider under the list's final row. */
export function Row({ left, title, sub, right, onPress, label, last }: { left: ReactNode; title: string; sub?: string; right?: ReactNode; onPress?: () => void; label?: string; last?: boolean }) {
  return <Pressable accessibilityRole={onPress ? 'button' : undefined} accessibilityLabel={label ?? title} onPress={onPress}
    style={({ pressed }) => [s.rowItem, last && { borderBottomWidth: 0 }, pressed && onPress ? { backgroundColor: c.raised } : null]}>
    {left}
    <View style={s.main}><Text numberOfLines={1} style={s.title}>{title}</Text>{sub ? <Text numberOfLines={1} style={s.sub}>{sub}</Text> : null}</View>
    {right}
  </Pressable>;
}

export function LargeTitle({ children, right }: { children: string; right?: ReactNode }) {
  return <View style={s.navbar}><Text accessibilityRole="header" style={s.large}>{children}</Text>{right}</View>;
}

/** The yellow strip a dropped connection shows. */
export function Banner({ children, busy }: { children: string; busy?: boolean }) {
  return <View accessibilityLiveRegion="polite" style={s.banner}>{busy ? <ActivityIndicator size="small" color={c.warn} /> : null}<Text style={s.bannerText}>{children}</Text></View>;
}

export function Empty({ children }: { children: string }) { return <Text style={s.empty}>{children}</Text>; }

const s = StyleSheet.create({
  avatar: { alignItems: 'center', justifyContent: 'center' },
  avatarText: { fontFamily: mono, fontWeight: '700', color: c.ground },
  spin: { width: 10, height: 10, borderRadius: 5, borderWidth: 2, marginRight: 4 },
  pill: { borderRadius: 999, paddingHorizontal: 8, paddingVertical: 3 },
  pillText: { fontSize: 11, fontWeight: '600' },
  row: { flexDirection: 'row', alignItems: 'center' },
  group: { flexDirection: 'row', justifyContent: 'space-between', marginTop: 14, marginBottom: 6, marginHorizontal: 6 },
  groupText: { color: c.faint, fontSize: 12, fontWeight: '600', letterSpacing: 0.7 },
  groupRight: { fontSize: 12 },
  list: { backgroundColor: c.surface, borderRadius: 14, overflow: 'hidden' },
  rowItem: { flexDirection: 'row', alignItems: 'center', gap: 12, paddingHorizontal: 12, paddingVertical: 11, minHeight: 60, borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: c.line },
  main: { flex: 1, minWidth: 0, gap: 2 },
  title: { color: c.text, fontSize: 16, fontWeight: '600' },
  sub: { color: c.muted, fontSize: 13 },
  navbar: { flexDirection: 'row', alignItems: 'flex-end', justifyContent: 'space-between', paddingHorizontal: 18, paddingTop: 4, paddingBottom: 10 },
  large: { color: c.text, fontSize: 30, fontWeight: '700', letterSpacing: -0.6 },
  banner: { flexDirection: 'row', gap: 8, alignItems: 'center', marginHorizontal: 14, marginBottom: 8, paddingHorizontal: 12, paddingVertical: 8, borderRadius: 10, backgroundColor: 'rgba(251,191,36,0.14)' },
  bannerText: { color: c.warn, fontSize: 13, flexShrink: 1 },
  empty: { color: c.muted, fontSize: 15, lineHeight: 21, textAlign: 'center', paddingHorizontal: 24, paddingVertical: 40 },
});
