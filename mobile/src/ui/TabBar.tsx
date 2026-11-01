import type { ReactElement } from 'react';
import { Keyboard, Pressable, StyleSheet, Text, View } from 'react-native';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import type { BottomTabBarProps } from 'expo-router/js-tabs';
import Svg, { Path, Rect } from './svg';
import { usePartyline, type Tab } from '../runtime';
import { c } from './theme';
import { useKeyboardVisible } from './useKeyboardVisible';

const icons: Record<string, (colour: string) => ReactElement> = {
  index: k => <Svg><Rect x="3" y="4" width="18" height="16" rx="3" stroke={k} /><Path d="M7 9l3 3-3 3M12 15h5" stroke={k} /></Svg>,
  loops: k => <Svg><Path d="M4 12a8 8 0 0 1 14-5.3M20 12a8 8 0 0 1-14 5.3" stroke={k} /><Path d="M18 3v4h-4M6 21v-4h4" stroke={k} /></Svg>,
  chat: k => <Svg><Path d="M4 5h16v11H9l-5 4z" stroke={k} /></Svg>,
  computers: k => <Svg><Rect x="3" y="4" width="18" height="12" rx="2" stroke={k} /><Path d="M8 20h8M12 16v4" stroke={k} /></Svg>,
};

/** Sessions, Loops, Chat, Computers; the Sessions tab counts the sessions waiting on you. */
export function TabBar({ state, descriptors, navigateToTab }: BottomTabBarProps) {
  const app = usePartyline();
  const insets = useSafeAreaInsets();
  const keyboardVisible = useKeyboardVisible();
  const waiting = app.sessions.filter(x => x.state === 'waiting').length;
  return <View style={[s.bar, { paddingBottom: keyboardVisible ? 8 : Math.max(insets.bottom, 10) }]}>
    {state.routes.map((route, i) => {
      const focused = state.index === i;
      const colour = focused ? c.accent : c.faint;
      const title = descriptors[route.key].options.title ?? route.name;
      const badge = route.name === 'index' && waiting > 0 ? waiting : 0;
      return <Pressable key={route.key} accessibilityRole="tab" accessibilityState={{ selected: focused }}
        accessibilityLabel={badge ? `${title}, ${badge} waiting on you` : title} style={s.tab}
        onPress={() => {
          Keyboard.dismiss();
          if (focused) return;
          // Recorded on a tap, so reopening the app lands on this tab. Not on focus: the runtime's
          // selectTab also clears the open session, and focus fires while a restored one opens.
          app.selectTab(route.name === 'index' ? 'sessions' : (route.name as Tab));
          navigateToTab(route.key);
        }}>
        <View>{icons[route.name]?.(colour)}{badge ? <View style={s.badge}><Text style={s.badgeText}>{badge}</Text></View> : null}</View>
        <Text style={[s.label, { color: colour }]}>{title}</Text>
      </Pressable>;
    })}
  </View>;
}

const s = StyleSheet.create({
  bar: { flexDirection: 'row', paddingTop: 8, paddingHorizontal: 8, backgroundColor: c.surface, borderTopWidth: StyleSheet.hairlineWidth, borderTopColor: c.line },
  tab: { flex: 1, alignItems: 'center', gap: 3, minHeight: 44 },
  label: { fontSize: 10, fontWeight: '600' },
  badge: { position: 'absolute', top: -4, left: 16, backgroundColor: c.accent, borderRadius: 999, paddingHorizontal: 5, paddingVertical: 1 },
  badgeText: { color: c.ground, fontSize: 10, fontWeight: '700' },
});
