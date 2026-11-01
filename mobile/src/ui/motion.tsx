import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react';
import { AccessibilityInfo, Animated, AppState, Easing } from 'react-native';
import { useFocusEffect } from 'expo-router';

const MotionAllowed = createContext(false);

/** One set of OS listeners for the app. Stay still until the accessibility preference resolves. */
export function MotionProvider({ children }: { children: ReactNode }) {
  const [reduced, setReduced] = useState(true);
  const [foreground, setForeground] = useState(AppState.currentState === 'active');
  useEffect(() => {
    let disposed = false, changed = false;
    const preference = AccessibilityInfo.addEventListener('reduceMotionChanged', value => { changed = true; setReduced(value); });
    void AccessibilityInfo.isReduceMotionEnabled().then(value => { if (!disposed && !changed) setReduced(value); }).catch(() => {});
    const activity = AppState.addEventListener('change', value => setForeground(value === 'active'));
    return () => { disposed = true; preference.remove(); activity.remove(); };
  }, []);
  return <MotionAllowed.Provider value={!reduced && foreground}>{children}</MotionAllowed.Provider>;
}

/** Cached session content must not imply that its tools are still running. */
export function MotionGate({ active, children }: { active: boolean; children?: ReactNode }) {
  const allowed = useContext(MotionAllowed);
  return <MotionAllowed.Provider value={allowed && active}>{children}</MotionAllowed.Provider>;
}

/** Native transforms/opacity only; hidden routes and settled states do no animation work. */
export function useSignalMotion(active: boolean, duration = 1400) {
  const allowed = useContext(MotionAllowed);
  const [focused, setFocused] = useState(false);
  const [value] = useState(() => new Animated.Value(0));
  useFocusEffect(useCallback(() => { setFocused(true); return () => setFocused(false); }, []));
  const running = active && allowed && focused;
  useEffect(() => {
    if (!running) return;
    const loop = Animated.loop(Animated.timing(value, { toValue: 1, duration, easing: Easing.linear, useNativeDriver: true, isInteraction: false }));
    loop.start();
    return () => { loop.stop(); value.setValue(0); };
  }, [running, duration, value]);
  return { value, running };
}
