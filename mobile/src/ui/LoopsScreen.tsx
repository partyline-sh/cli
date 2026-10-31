import { useState } from 'react';
import { Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { usePartyline, type LoopItem } from '../runtime';
import { Empty, LargeTitle } from './parts';
import { age, c, mono } from './theme';

// The board's own column names, in the order work moves. A column the board adds later is kept,
// after these, rather than its cards dropped.
const order = ['Needs you', 'In progress', 'In review', 'Planned', 'Done'];
const maxRounds = 6;

function Card({ l }: { l: LoopItem }) {
  const pips = l.round ? '●'.repeat(Math.min(l.round, maxRounds)) + '○'.repeat(Math.max(0, maxRounds - l.round)) : '';
  return <View accessible accessibilityLabel={`${l.title}, ${l.state}${l.round ? `, round ${l.round}` : ''}`} style={s.card}>
    <View style={s.top}><Text numberOfLines={1} style={s.id}>{[l.id, l.repo].filter(Boolean).join(' · ')}</Text><Text style={s.pips}>{pips}</Text></View>
    <Text style={s.title}>{l.title}</Text>
    <View style={s.meta}>
      <Text style={[s.metaText, l.section === 'Needs you' && { color: c.accent }]}>{l.state}</Text>
      {l.detail ? <Text numberOfLines={2} style={[s.metaText, s.detail]}>{l.detail}</Text> : null}
      {l.updatedAt ? <Text style={s.metaText}>{age(l.updatedAt)}</Text> : null}
    </View>
  </View>;
}

/** Select any combination of board stages; live card updates retain that selection. */
export function LoopsScreen() {
  const app = usePartyline();
  const [picked, setPicked] = useState<readonly string[] | 'all' | undefined>();
  const sections = [...order, ...[...new Set([...app.loops.map(l => l.section), ...(picked === 'all' ? [] : picked ?? [])])].filter(x => !order.includes(x))];
  const counts = (x: string) => app.loops.filter(l => l.section === x).length;
  const first = sections.find(x => counts(x) > 0) ?? 'In progress';
  const selected = picked === 'all' ? sections : picked ?? [first];
  const all = sections.every(x => selected.includes(x));
  const groups = sections.filter(x => selected.includes(x)).map(section => ({ section, cards: app.loops.filter(l => l.section === section) }));
  const count = groups.reduce((n, group) => n + group.cards.length, 0);
  const toggle = (section: string) => setPicked(previous => {
    const current = previous === 'all' ? sections : previous ?? [first];
    return current.includes(section) ? current.filter(x => x !== section) : [...current, section];
  });
  return <SafeAreaView edges={['top']} style={s.screen}>
    <LargeTitle>Loops</LargeTitle>
    <View style={s.chips}>
      <Pressable accessibilityRole="checkbox" accessibilityLabel="All stages" accessibilityState={{ checked: all ? true : selected.length ? 'mixed' : false }}
        onPress={() => setPicked(all ? [] : 'all')} style={[s.chip, all && s.chipOn]}><Text style={[s.chipText, all && { color: c.ground }]}>All</Text></Pressable>
      {sections.map(x => <Pressable key={x} accessibilityRole="checkbox" accessibilityLabel={x} accessibilityState={{ checked: selected.includes(x) }} onPress={() => toggle(x)} style={[s.chip, selected.includes(x) && s.chipOn]}>
        <Text style={[s.chipText, selected.includes(x) && { color: c.ground }]}>{`${selected.includes(x) ? '✓ ' : ''}${x} ${counts(x)}`}</Text></Pressable>)}
    </View>
    <ScrollView contentContainerStyle={s.list}>
      {count ? groups.filter(group => group.cards.length).map(group => <View key={group.section} style={s.group}>
        {selected.length > 1 ? <Text style={s.heading}>{`${group.section} · ${group.cards.length}`}</Text> : null}
        {group.cards.map((l, i) => <Card key={l.id ? `${l.computerId}:${l.id}` : `${l.computerId}:${l.section}:${i}:${l.title}`} l={l} />)}
      </View>) : <Empty>{!selected.length ? 'Select one or more stages.' : app.loops.length ? 'No loops in the selected stages.' : 'No loops on your computers.'}</Empty>}
    </ScrollView>
  </SafeAreaView>;
}

const s = StyleSheet.create({
  screen: { flex: 1, backgroundColor: c.ground },
  chips: { flexDirection: 'row', flexWrap: 'wrap', gap: 6, paddingHorizontal: 16, paddingBottom: 10 },
  chip: { borderRadius: 999, paddingHorizontal: 12, minHeight: 34, justifyContent: 'center', backgroundColor: c.raised },
  chipOn: { backgroundColor: c.text },
  chipText: { color: c.muted, fontSize: 13, fontWeight: '600' },
  list: { paddingHorizontal: 14, paddingBottom: 16, gap: 10 },
  group: { gap: 10 },
  heading: { color: c.muted, fontSize: 12, fontWeight: '600', marginTop: 4 },
  card: { backgroundColor: c.surface, borderRadius: 14, padding: 12, gap: 6 },
  top: { flexDirection: 'row', justifyContent: 'space-between', gap: 8 },
  id: { color: c.muted, fontFamily: mono, fontSize: 12, flexShrink: 1 },
  pips: { color: c.claude, fontFamily: mono, fontSize: 12, letterSpacing: 1 },
  title: { color: c.text, fontSize: 15, fontWeight: '700', lineHeight: 20 },
  meta: { flexDirection: 'row', flexWrap: 'wrap', gap: 10 },
  metaText: { color: c.muted, fontSize: 12 },
  detail: { flexBasis: '100%' },
});
