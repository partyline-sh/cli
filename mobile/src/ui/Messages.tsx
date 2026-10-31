import { useRef, useState } from 'react';
import { Pressable, ScrollView, StyleSheet, Text, View, type NativeScrollEvent, type NativeSyntheticEvent, type ScrollViewInstance } from 'react-native';
import type { Tool, TranscriptEntry } from '../runtime';
import { Spinner } from './parts';
import { c, mono } from './theme';

function ToolLine({ tool }: { tool: Tool }) {
  const [open, setOpen] = useState(false);
  const status = tool.status === 'running' ? <View style={s.statusRow}><Spinner colour={c.muted} /><Text style={s.status}>running</Text></View>
    : tool.status === 'ok' ? <Text style={[s.status, { color: c.ok }]}>ok</Text>
    : tool.status === 'error' ? <Text style={[s.status, { color: c.warn }]}>error</Text> : null;
  return <Pressable accessibilityRole="button" accessibilityLabel={`${tool.name} ${tool.target ?? ''} ${tool.status ?? ''}`}
    onPress={() => setOpen(o => !o)} style={s.tool}>
    <Text numberOfLines={open ? undefined : 1} style={s.toolText}><Text style={s.toolName}>{tool.name}</Text>{tool.target ? ` ${tool.target}` : ''}</Text>
    {status}
  </Pressable>;
}

/** The session as a conversation: your prompts on the right, replies on the left, a line per tool run. */
export function Messages({ entries, stale }: { entries: readonly TranscriptEntry[]; stale?: string }) {
  const ref = useRef<ScrollViewInstance>(null);
  const atEnd = useRef(true); // follow new entries only while the reader is at the bottom
  const onScroll = (e: NativeSyntheticEvent<NativeScrollEvent>) => {
    const { contentOffset, contentSize, layoutMeasurement } = e.nativeEvent;
    atEnd.current = contentOffset.y + layoutMeasurement.height >= contentSize.height - 40;
  };
  return <ScrollView ref={ref} style={s.list} contentContainerStyle={s.content} onScroll={onScroll} scrollEventThrottle={100}
    onContentSizeChange={() => { if (atEnd.current) ref.current?.scrollToEnd({ animated: false }); }}>
    {stale ? <Text style={s.stale}>{stale}</Text> : null}
    {entries.length === 0 ? <Text style={s.stale}>Nothing said in this session yet.</Text> : null}
    {entries.map(e => e.role === 'user'
      ? <Text key={e.id} selectable style={s.me}>{e.text}</Text>
      : <View key={e.id} style={s.agent}>
          {e.text ? <Text selectable style={s.reply}>{e.text}</Text> : null}
          {e.tools.map((t, i) => <ToolLine key={i} tool={t} />)}
          {e.truncated ? <Text style={s.cut}>Cut short; the terminal has all of it.</Text> : null}
        </View>)}
  </ScrollView>;
}

const s = StyleSheet.create({
  list: { flex: 1 },
  content: { padding: 12, gap: 10 },
  me: { alignSelf: 'flex-end', maxWidth: '82%', backgroundColor: c.accent, color: c.ground, paddingHorizontal: 13, paddingVertical: 9, borderRadius: 18, borderBottomRightRadius: 4, fontSize: 15, lineHeight: 20, overflow: 'hidden' },
  agent: { alignSelf: 'flex-start', maxWidth: '92%', gap: 6 },
  reply: { backgroundColor: c.surface, color: c.text, paddingHorizontal: 13, paddingVertical: 9, borderRadius: 18, borderBottomLeftRadius: 4, fontSize: 15, lineHeight: 21, overflow: 'hidden' },
  tool: { flexDirection: 'row', justifyContent: 'space-between', gap: 8, backgroundColor: c.term, borderWidth: StyleSheet.hairlineWidth, borderColor: c.line, borderRadius: 10, paddingHorizontal: 10, paddingVertical: 7 },
  toolText: { flex: 1, color: c.muted, fontFamily: mono, fontSize: 12 },
  toolName: { color: c.text, fontWeight: '600' },
  statusRow: { flexDirection: 'row', alignItems: 'center' },
  status: { color: c.muted, fontFamily: mono, fontSize: 12 },
  stale: { color: c.faint, fontSize: 12, textAlign: 'center' },
  cut: { color: c.faint, fontSize: 12 },
});
