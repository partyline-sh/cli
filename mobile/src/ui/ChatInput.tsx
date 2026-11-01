import { useRef, useState, type ComponentRef } from 'react';
import { Platform, Pressable, ScrollView, StyleSheet, Text, TextInput, View, type StyleProp, type TextStyle } from 'react-native';
import type { ChatRoomState } from '../runtime/types';
import { insertMention, mentionOptions, mentionToken, type Caret, type Mention } from './chat-mentions';
import { c, mono } from './theme';

/** The room composer only. Session terminal input keeps its existing handling. */
export function ChatInput({ value, onChangeText, placeholder, style, room }: { value: string; onChangeText: (value: string) => void; placeholder: string; style: StyleProp<TextStyle>; room?: ChatRoomState }) {
  const input = useRef<ComponentRef<typeof TextInput>>(null);
  const [caret, setCaret] = useState<Caret>({ start: value.length, end: value.length });
  const [closed, setClosed] = useState('');
  const [selected, setSelected] = useState(0);
  const range = mentionToken(value, caret);
  const identity = `${value}:${caret.start}:${caret.end}`;
  const options = range && closed !== identity ? mentionOptions(room, range.query) : [];
  const at = Math.min(selected, Math.max(options.length - 1, 0));
  const choose = (option: Mention) => {
    if (!range || !options.some(item => item.token === option.token)) return;
    const next = insertMention(value, range, option);
    onChangeText(next.text); setCaret(next.caret); setSelected(0); input.current?.focus();
  };
  return <View style={s.wrap}>
    {options.length ? <View style={s.picker}>
      <View style={s.heading}><Text style={s.hint}>Mention a room member</Text><Pressable accessibilityRole="button" accessibilityLabel="Dismiss mention suggestions" onPress={() => setClosed(identity)} style={s.dismiss}><Text style={s.close}>×</Text></Pressable></View>
      <ScrollView style={s.scroll} keyboardShouldPersistTaps="always" nestedScrollEnabled>{options.map((option, i) => <Pressable key={option.token} accessibilityRole="button" accessibilityLabel={`Insert ${option.token}, ${option.label}`} accessibilityState={{ selected: i === at }} onPress={() => choose(option)} style={[s.option, i === at && s.selected]}>
        <Text style={s.token}>{option.token}</Text><Text style={s.label}>{option.label}</Text>
      </Pressable>)}</ScrollView>
    </View> : null}
    <TextInput ref={input} value={value} onChangeText={text => {
      const end = Math.min(text.length, Math.max(0, caret.start + text.length - value.length));
      setCaret({ start: end, end }); setSelected(0); onChangeText(text);
    }} selection={{ start: Math.min(caret.start, value.length), end: Math.min(caret.end, value.length) }} onSelectionChange={event => setCaret(event.nativeEvent.selection)} onKeyPress={event => {
      // Native keypress notifications cannot cancel the keyboard's newline insertion.
      // Native users select a suggestion by touch or its accessible button.
      if (Platform.OS !== 'web' || !options.length) return;
      switch (event.nativeEvent.key) {
        case 'ArrowDown': event.preventDefault(); setSelected((at + 1) % options.length); break;
        case 'ArrowUp': event.preventDefault(); setSelected((at + options.length - 1) % options.length); break;
        case 'Tab': case 'Enter': event.preventDefault(); choose(options[at]); break;
        case 'Escape': event.preventDefault(); setClosed(identity); break;
      }
    }} placeholder={placeholder} placeholderTextColor={c.faint} multiline style={[style, s.input]} accessibilityLabel={placeholder} />
  </View>;
}
const s = StyleSheet.create({
  wrap: { flex: 1, minWidth: 0 }, input: { flex: 0 },
  picker: { borderWidth: StyleSheet.hairlineWidth, borderColor: c.line, backgroundColor: c.surface, borderRadius: 10, marginBottom: 6, overflow: 'hidden' },
  heading: { flexDirection: 'row', alignItems: 'center', paddingLeft: 10 }, hint: { color: c.muted, fontSize: 12, flex: 1 },
  dismiss: { minWidth: 44, minHeight: 36, alignItems: 'center', justifyContent: 'center' }, close: { color: c.muted, fontSize: 22 },
  scroll: { maxHeight: 160 }, option: { paddingHorizontal: 10, paddingVertical: 9, minHeight: 44, gap: 2 }, selected: { backgroundColor: c.raised },
  token: { color: c.accent, fontFamily: mono, fontSize: 13, fontWeight: '600' }, label: { color: c.muted, fontSize: 12 },
});
