import { useState } from 'react';
import { Pressable, StyleSheet, Text, View } from 'react-native';
import type { LoopItem } from '../runtime/types';
import { loopProgress, type StageName } from './loop-progress';
import { Signal } from './Signal';
import { c, mono } from './theme';

export function LoopProgress({ loop, connected, compact = false }: { loop: LoopItem; connected: boolean; compact?: boolean }) {
  const [selected, setSelected] = useState<StageName>();
  const { stages, moving } = loopProgress(loop, connected);
  const details = selected === 'Build' ? `Builder: ${loop.builder || 'not reported'}${loop.round ? ` · round ${loop.round}` : ''}`
    : selected === 'Checks' ? (loop.chips?.filter(x => x.name === 'checks').map(x => `${x.name}: ${x.mark}`).join(' · ') || 'Check results not reported.')
    : (loop.chips?.filter(x => x.name !== 'checks').map(x => `${x.name}: ${x.mark}`).join(' · ') || 'Reviewer results not reported.');
  return <View style={s.wrap}>
    <View style={s.stages}>{stages.map(stage => {
      const active = stage.mark === 'active';
      const colour = !connected ? c.faint : stage.mark === 'blocked' ? c.warn : active ? c.claude : stage.mark === 'done' ? c.gemini : c.faint;
      const content = <><Signal mode={active && moving ? 'active' : active || stage.mark === 'blocked' ? 'waiting' : stage.mark === 'done' ? 'confirmed' : 'idle'} colour={colour} /><Text style={[s.name, { color: colour }]}>{stage.name}</Text></>;
      return compact ? <View key={stage.name} style={s.stage}>{content}</View> : <Pressable key={stage.name} onPress={() => setSelected(stage.name)} accessibilityRole="button" accessibilityLabel={`${stage.name}: ${active && !moving ? 'last reported active' : stage.mark}. Show details.`} accessibilityState={{ selected: selected === stage.name }} style={[s.stage, selected === stage.name && s.selected]}>{content}</Pressable>;
    })}</View>
    {!compact ? <Text style={s.detail}>{!connected ? 'Offline · last reported state. ' : ''}{selected ? details : 'Tap a stage for reported details. ✓ means finished, not necessarily passed.'}</Text> : null}
  </View>;
}
const s = StyleSheet.create({
  wrap: { gap: 8 }, stages: { flexDirection: 'row', gap: 6 },
  stage: { flex: 1, minHeight: 44, flexDirection: 'row', alignItems: 'center', justifyContent: 'center', gap: 4, backgroundColor: c.ground, borderRadius: 6, borderWidth: 1, borderColor: 'transparent' },
  selected: { borderColor: c.claude }, name: { fontFamily: mono, fontSize: 10 },
  detail: { color: c.muted, fontSize: 12, lineHeight: 18 },
});
