import { useEffect, useRef, useState } from 'react';
import { Pressable, ScrollView, StyleSheet, Text, View, type NativeScrollEvent, type NativeSyntheticEvent, type ScrollViewInstance } from 'react-native';
import { ansiSpans } from './ansi';
import { c, mono } from './theme';
import { fitTerminalFont, phoneTerminalViewport, terminalSnapshotText, type TerminalViewport } from './terminal-layout';

const sizes = [10, 11.5, 13, 15];

/** The pane as tmux shows it, at one of four text sizes. Rows are never wrapped: wide panes scroll
 * sideways, so tables and full-screen programs keep their shape. New snapshots keep the reader at
 * the bottom only if they were already there. */
export function Terminal({ ansi, size, columns, onViewportChange }: { ansi: string; size: number; columns?: number; onViewportChange?: (viewport: TerminalViewport) => void }) {
  const ref = useRef<ScrollViewInstance>(null);
  const atEnd = useRef(true);
  const preferredFont = sizes[Math.min(Math.max(size, 0), sizes.length - 1)];
  const [fit, setFit] = useState(false);
  const [viewport, setViewport] = useState({ width: 0, height: 0 });
  const [measurement, setMeasurement] = useState<{ font: number; cell: number; line: number }>();
  const cell = measurement ? measurement.cell * preferredFont / measurement.font : preferredFont * 0.6;
  const line = measurement ? measurement.line * preferredFont / measurement.font : preferredFont * 1.55;
  const phone = phoneTerminalViewport(viewport.width, viewport.height, cell, line);
  useEffect(() => {
    if (phone) onViewportChange?.(phone);
  }, [phone?.columns, phone?.rows, onViewportChange]); // eslint-disable-line react-hooks/exhaustive-deps
  const font = fit ? fitTerminalFont(viewport.width, columns, preferredFont, cell) : preferredFont;
  const onScroll = (e: NativeSyntheticEvent<NativeScrollEvent>) => {
    const { contentOffset, contentSize, layoutMeasurement } = e.nativeEvent;
    atEnd.current = contentOffset.y + layoutMeasurement.height >= contentSize.height - 40;
  };
  // A monospace cell is about 0.6 of the font size wide: the pane's full width, so a short snapshot
  // does not shrink the scroll area below the pane's own columns.
  const width = columns ? Math.ceil(columns * cell * font / preferredFont) + 24 : undefined;
  return <View style={s.term}>
    <View style={s.tools}>
      {([false, true] as const).map(value => <Pressable key={String(value)} accessibilityRole="radio" accessibilityState={{ checked: fit === value }}
        accessibilityLabel={value ? 'Fit terminal width' : 'Readable terminal text'} onPress={() => setFit(value)} style={[s.tool, fit === value && s.toolOn]}>
        <Text style={[s.toolText, fit === value && { color: c.text }]}>{value ? 'Fit width' : 'Readable'}</Text>
      </Pressable>)}
      {columns ? <Text style={s.dimensions}>{`${columns} columns`}</Text> : null}
    </View>
    <Text accessibilityElementsHidden importantForAccessibility="no-hide-descendants" pointerEvents="none"
      style={[s.text, s.measure, { fontSize: preferredFont, lineHeight: preferredFont * 1.55 }]}
      onTextLayout={e => {
        const measured = e.nativeEvent.lines[0];
        if (measured?.width > 0 && measured.height > 0) setMeasurement(previous => {
          const nextLine = e.nativeEvent.lines[1];
          const advance = nextLine ? nextLine.y - measured.y : measured.height;
          const next = { font: preferredFont, cell: measured.width / 16, line: advance > 0 ? advance : preferredFont * 1.55 };
          return previous && previous.font === next.font && Math.abs(previous.cell - next.cell) < 0.01 && Math.abs(previous.line - next.line) < 0.01 ? previous : next;
        });
      }}>{'MMMMMMMMMMMMMMMM\nMMMMMMMMMMMMMMMM'}</Text>
    <ScrollView ref={ref} style={s.term} onScroll={onScroll} scrollEventThrottle={100}
      onLayout={e => { const { width, height } = e.nativeEvent.layout; setViewport(previous => previous.width === width && previous.height === height ? previous : { width, height }); }}
    onContentSizeChange={() => { if (atEnd.current) ref.current?.scrollToEnd({ animated: false }); }}>
    <ScrollView horizontal showsHorizontalScrollIndicator contentContainerStyle={[s.content, width ? { minWidth: width } : null]}>
      <Text selectable style={[s.text, { fontSize: font, lineHeight: font * 1.55 }]}>
        {ansiSpans(terminalSnapshotText(ansi), { fg: c.termText, bg: c.term }).map((x, i) => <Text key={i} style={{ color: x.fg ?? c.termText, backgroundColor: x.bg, fontWeight: x.bold ? '700' : '400', opacity: x.dim ? 0.6 : 1 }}>{x.text}</Text>)}
      </Text>
    </ScrollView>
  </ScrollView></View>;
}
export const terminalSizes = sizes.length;

const s = StyleSheet.create({
  term: { flex: 1, backgroundColor: c.term },
  tools: { flexDirection: 'row', alignItems: 'center', gap: 6, paddingHorizontal: 12, paddingVertical: 6 },
  tool: { minHeight: 34, paddingHorizontal: 10, justifyContent: 'center', borderRadius: 8 },
  toolOn: { backgroundColor: c.raised },
  toolText: { color: c.muted, fontSize: 12, fontWeight: '600' },
  dimensions: { color: c.faint, fontSize: 11, marginLeft: 'auto' },
  measure: { position: 'absolute', opacity: 0, left: 0, top: 0, width: 10000 },
  content: { padding: 12 },
  text: { fontFamily: mono, color: c.termText },
});
