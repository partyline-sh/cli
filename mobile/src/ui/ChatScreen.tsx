import { useRef, useState } from 'react';
import { Platform, Pressable, ScrollView, StyleSheet, Text, View, type ScrollViewInstance } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { usePartyline, type ChatMessage } from '../runtime';
import { Composer } from './Composer';
import { Avatar, Empty, LargeTitle } from './parts';
import { c, clock, mono } from './theme';

/** "#12 claude" → claude; the person's own messages have no engine. */
const engineOf = (m: ChatMessage) => (m.own ? 'you' : (m.name.match(/(claude|codex|gemini)/i)?.[1] ?? m.name).toLowerCase());

/** Code spans in backticks, in mono; the rest as text. Markdown beyond that is shown as written. */
function Body({ text }: { text: string }) {
  return <Text selectable style={s.text}>{text.split(/(`[^`\n]+`)/).map((part, i) =>
    part.startsWith('`') && part.endsWith('`') && part.length > 2 ? <Text key={i} style={s.code}>{part.slice(1, -1)}</Text> : part)}</Text>;
}

/** The room each computer's sessions share with you, with a box to write to it. */
export function ChatScreen() {
  const app = usePartyline();
  const rooms = app.computers.filter(k => app.chat.some(m => m.computerId === k.id) || k.connection === 'connected');
  const [picked, setPicked] = useState<string | undefined>();
  const room = rooms.find(k => k.id === picked) ?? rooms[0];
  const messages = room ? app.chat.filter(m => m.computerId === room.id) : [];
  const ref = useRef<ScrollViewInstance>(null);
  return <SafeAreaView edges={['top']} style={s.screen}>
    <LargeTitle>Chat</LargeTitle>
    {rooms.length > 1 ? <View style={s.chips}>{rooms.map(k => <Pressable key={k.id} accessibilityRole="tab" accessibilityState={{ selected: k.id === room?.id }}
      onPress={() => setPicked(k.id)} style={[s.chip, k.id === room?.id && s.chipOn]}><Text style={[s.chipText, k.id === room?.id && { color: c.ground }]}>{k.name}</Text></Pressable>)}</View> : null}
    <View style={s.body}>
      <ScrollView ref={ref} contentContainerStyle={s.list} keyboardDismissMode={Platform.OS === 'ios' ? 'interactive' : 'on-drag'} keyboardShouldPersistTaps="handled" onContentSizeChange={() => ref.current?.scrollToEnd({ animated: false })}>
        {!room ? <Empty>Pair a computer to see its chat room.</Empty> : messages.length === 0 ? <Empty>No messages yet.</Empty> : null}
        {messages.map(m => <View key={m.id} style={s.msg}>
          <Avatar engine={engineOf(m)} size={30} label={m.own ? 'Y' : undefined} />
          <View style={s.main}>
            <Text><Text style={s.name}>{m.own ? 'you' : m.name}</Text><Text style={s.time}>{`  ${clock(Date.parse(m.at))}`}</Text></Text>
            <Body text={m.text} />
          </View>
        </View>)}
      </ScrollView>
      {room ? <Composer computerId={room.id} pane="chat" placeholder="Message the room…" /> : null}
    </View>
  </SafeAreaView>;
}

const s = StyleSheet.create({
  screen: { flex: 1, backgroundColor: c.ground },
  body: { flex: 1 },
  chips: { flexDirection: 'row', gap: 6, paddingHorizontal: 16, paddingBottom: 8 },
  chip: { borderRadius: 999, paddingHorizontal: 12, minHeight: 34, justifyContent: 'center', backgroundColor: c.raised },
  chipOn: { backgroundColor: c.text },
  chipText: { color: c.muted, fontSize: 13, fontWeight: '600' },
  list: { paddingHorizontal: 16, paddingBottom: 12, gap: 12 },
  msg: { flexDirection: 'row', gap: 10 },
  main: { flex: 1, minWidth: 0 },
  name: { color: c.text, fontSize: 13, fontWeight: '700' },
  time: { color: c.faint, fontSize: 11 },
  text: { color: c.text, fontSize: 15, lineHeight: 21, marginTop: 2 },
  code: { fontFamily: mono, fontSize: 13, color: c.claude },
});
