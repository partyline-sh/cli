import { StyleSheet, Text } from 'react-native';
import { c } from './theme';
export function savedViewLabel(at: number, connected: boolean) {
    const stamp = new Date(at).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
    return `Saved view (read-only) · last updated ${stamp} · ${connected ? 'syncing…' : 'waiting for connection'}`;
}
export function HistoryStatus({ at, connected }: {
    at: number;
    connected: boolean;
}) {
    return <Text accessibilityLiveRegion="polite" style={s.status}>{savedViewLabel(at, connected)}</Text>;
}
const s = StyleSheet.create({ status: { color: c.warn, fontSize: 12, paddingHorizontal: 16, paddingVertical: 8 } });
