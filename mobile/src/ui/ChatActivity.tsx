import { ScrollView, StyleSheet, Text, View } from 'react-native';
import type { ChatMessage, ChatRoomState, Computer, Session } from '../runtime/types';
import { ChatLimitMeter } from './ChatLimitMeter';
import { ConnectionSignal } from './ConnectionSignal';
import { Signal } from './Signal';
import { memberFeedback } from './chat-feedback';
import { c } from './theme';

export function ChatActivity({ computer, room, sessions, messages }: { computer: Computer; room?: ChatRoomState; sessions: readonly Session[]; messages: readonly ChatMessage[] }) {
  const connected = computer.connection === 'connected';
  const local = sessions.filter(s => s.computerId === computer.id);
  return <View style={s.wrap}>
    <View style={s.connection}><Text numberOfLines={1} style={s.host}>{computer.name}</Text><ConnectionSignal state={computer.connection} /></View>
    {connected ? <ChatLimitMeter computer={computer} room={room} /> : null}
    {!connected ? <Text style={s.hint}>Messages shown below may be out of date. Live updates resume when connected.</Text>
      : !room ? <Text style={s.hint}>Live room activity has not been reported yet.</Text>
      : room.members.length === 0 ? <Text style={s.hint}>No agents in this room.</Text>
      : <ScrollView style={s.scroll} contentContainerStyle={s.members}>{room.members.map(member => {
        const feedback = memberFeedback(member, room, local, messages, connected);
        return <View key={member.key} style={s.member}>
          <Signal mode={feedback.mode} colour={feedback.mode === 'active' ? c.ok : feedback.mode === 'waiting' ? c.claude : c.muted} />
          <Text style={s.label}><Text style={s.name}>{member.name}</Text>{` · ${feedback.label}`}</Text>
        </View>;
      })}</ScrollView>}
  </View>;
}
const s = StyleSheet.create({
  wrap: { marginHorizontal: 14, marginBottom: 10, padding: 10, gap: 7, backgroundColor: c.surface, borderRadius: 10 },
  connection: { flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', gap: 12 },
  host: { color: c.muted, fontSize: 12, flex: 1 }, scroll: { maxHeight: 126 }, members: { gap: 3 },
  member: { flexDirection: 'row', alignItems: 'center', gap: 6 }, label: { color: c.muted, fontSize: 12, flex: 1 },
  name: { color: c.text, fontWeight: '600' }, hint: { color: c.muted, fontSize: 12, lineHeight: 17 },
});
