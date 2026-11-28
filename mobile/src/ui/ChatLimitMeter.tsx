import { Pressable, StyleSheet, Text, View } from 'react-native';
import { usePartyline } from '../runtime';
import type { ChatRoomState, Computer } from '../runtime/types';
import { chatLimitLabel, chatLimitView, nextChatLimit } from './chat-limit';
import { c } from './theme';

/** The deliveries queue keys limit changes by this pane, one per computer. */
export const CHAT_LIMIT_PANE = 'chat-limit';

/** How many replies the agents have used since you spoke, with − and + to change the limit. */
export function ChatLimitMeter({ computer, room }: { computer: Computer; room?: ChatRoomState }) {
  const app = usePartyline();
  const view = chatLimitView(room);
  if (!view) return null;
  const supported = computer.capabilities.includes('chat_limit') && !!app.setChatLimit;
  // The delivery, not the submit promise, is the truth: a change is pending until the computer
  // answers, and a later refusal or uncertain result still has to reach the screen.
  const last = app.deliveries.filter(d => d.computerId === computer.id && d.pane === CHAT_LIMIT_PANE).at(-1);
  const pending = last?.status === 'waiting';
  const problem = last?.status === 'refused' || last?.status === 'not-sent' ? last.reason || 'The computer did not change the limit'
    : last?.status === 'unknown' ? 'Not confirmed: the computer may not have changed the limit' : '';
  const canChange = supported && computer.connection === 'connected' && view.limit !== 'off' && !pending;
  const change = (direction: 1 | -1) => {
    const next = nextChatLimit(view, direction);
    if (next !== undefined && canChange) void app.setChatLimit!(computer.id, next).catch(() => {});
  };
  const lower = nextChatLimit(view, -1);
  return <View style={s.row}>
    <Text accessibilityLiveRegion="polite" style={[s.label, (view.paused || view.near || view.stopped) && s.warn]}>{chatLimitLabel(view)}</Text>
    {supported && view.limit !== 'off' ? <>
      <Pressable accessibilityRole="button" accessibilityLabel="Allow fewer agent replies" accessibilityState={{ disabled: !canChange || lower === undefined }} hitSlop={8} disabled={!canChange || lower === undefined} onPress={() => change(-1)} style={[s.key, (!canChange || lower === undefined) && s.off]}><Text style={s.keyText}>−</Text></Pressable>
      <Pressable accessibilityRole="button" accessibilityLabel="Allow more agent replies" accessibilityState={{ disabled: !canChange }} hitSlop={8} disabled={!canChange} onPress={() => change(1)} style={[s.key, !canChange && s.off]}><Text style={s.keyText}>+</Text></Pressable>
    </> : null}
    {pending ? <Text style={s.note}>Changing the limit…</Text> : problem ? <Text style={s.error}>{problem}</Text> : null}
  </View>;
}
const s = StyleSheet.create({
  row: { flexDirection: 'row', flexWrap: 'wrap', alignItems: 'center', gap: 8 },
  label: { color: c.muted, fontSize: 12, flex: 1 }, warn: { color: c.warn, fontWeight: '600' },
  key: { minWidth: 36, minHeight: 32, borderRadius: 8, backgroundColor: c.raised, alignItems: 'center', justifyContent: 'center' },
  off: { opacity: 0.4 }, keyText: { color: c.text, fontSize: 16, fontWeight: '700' },
  note: { color: c.muted, fontSize: 12, width: '100%' }, error: { color: c.warn, fontSize: 12, width: '100%' },
});
