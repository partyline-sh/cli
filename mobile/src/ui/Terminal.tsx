import { useEffect, useMemo, useRef, useState } from 'react';
import { Pressable, ScrollView, StyleSheet, Text, View, type NativeScrollEvent, type NativeSyntheticEvent, type ScrollViewInstance } from 'react-native';
import { ansiRows } from './ansi';
import { c, mono } from './theme';
import { fitTerminalFont, phoneTerminalViewport, terminalSnapshotText, type TerminalViewport } from './terminal-layout';

const sizes = [10, 11.5, 13, 15];

/** Phone-only wrapping is available for text. Exact grid and Fit width preserve terminal columns.
 * None of these display modes resize the computer. New snapshots follow the bottom only
 * when the reader was already there. */
export function Terminal({ ansi, size, columns, onViewportChange, defaultDisplay = 'grid', showControls = true }: { showControls?: boolean; defaultDisplay?: 'wrap' | 'grid'; ansi: string; size: number; columns?: number; onViewportChange?: (viewport: TerminalViewport) => void }) {
  const ref = useRef<ScrollViewInstance>(null);
  const atEnd = useRef(true);
  const userScrolling = useRef(false);
  const frame = useRef<number | undefined>(undefined);
  const [following, setFollowing] = useState(true);
  const followLatest = () => {
    if (!atEnd.current) return;
    if (frame.current !== undefined) cancelAnimationFrame(frame.current);
    frame.current = requestAnimationFrame(() => {
      frame.current = undefined;
      if (atEnd.current) ref.current?.scrollToEnd({ animated: false });
    });
  };
  useEffect(() => () => { if (frame.current !== undefined) cancelAnimationFrame(frame.current); }, []);
  const preferredFont = sizes[Math.min(Math.max(size, 0), sizes.length - 1)];
  const [choice, setChoice] = useState<'wrap' | 'grid' | 'fit'>();
  const layout = choice ?? defaultDisplay;
  const [viewport, setViewport] = useState({ width: 0, height: 0 });
  const [measurement, setMeasurement] = useState<{ font: number; cell: number; line: number }>();
  const cell = measurement ? measurement.cell * preferredFont / measurement.font : preferredFont * 0.6;
  const line = measurement ? measurement.line * preferredFont / measurement.font : preferredFont * 1.55;
  const phone = phoneTerminalViewport(viewport.width, viewport.height, cell, line);
  useEffect(() => {
    if (phone) onViewportChange?.(phone);
  }, [phone?.columns, phone?.rows, onViewportChange]); // eslint-disable-line react-hooks/exhaustive-deps
  const font = layout === 'fit' ? fitTerminalFont(viewport.width, columns, preferredFont, cell) : preferredFont;
  const onScroll = (e: NativeSyntheticEvent<NativeScrollEvent>) => {
    const { contentOffset, contentSize, layoutMeasurement } = e.nativeEvent;
    // Layout and programmatic scroll events are not a request to stop following.
    if (!userScrolling.current) return;
    atEnd.current = contentOffset.y + layoutMeasurement.height >= contentSize.height - 40;
    setFollowing(atEnd.current);
  };
  // A monospace cell is about 0.6 of the font size wide: the pane's full width, so a short snapshot
  // does not shrink the scroll area below the pane's own columns.
  const width = columns ? Math.ceil(columns * cell * font / preferredFont) + 24 : undefined;
  const rows = useMemo(() => ansiRows(terminalSnapshotText(ansi), { fg: c.termText, bg: c.term }), [ansi]);
  const contents = <View>{rows.map((row, index) => <Text key={index} selectable testID={`terminal-row-${index}`}
    style={[s.text, { fontSize: font, lineHeight: font * 1.55, minHeight: font * 1.55 }]}>
    {row.map((x, i) => <Text key={i} style={{ color: x.fg ?? c.termText, backgroundColor: x.bg, fontWeight: x.bold ? '700' : '400', opacity: x.dim ? 0.6 : 1 }}>{x.text}</Text>)}
  </Text>)}</View>;
  return <View style={s.term}>
    {showControls ? <View style={s.tools}>
      {(['wrap', 'grid', 'fit'] as const).map(value => <Pressable key={value} accessibilityRole="radio" accessibilityState={{ checked: layout === value }} aria-checked={layout === value}
        accessibilityLabel={value === 'wrap' ? 'Wrap terminal text on this phone' : value === 'grid' ? 'Exact terminal grid' : 'Fit terminal width'}
        onPress={() => setChoice(value)} style={[s.tool, layout === value && s.toolOn]}>
        <Text style={[s.toolText, layout === value && { color: c.text }]}>{value === 'wrap' ? 'Wrap text' : value === 'grid' ? 'Exact grid' : 'Fit width'}</Text>
      </Pressable>)}
      {columns ? <Text style={s.dimensions}>{`${columns} columns`}</Text> : null}
    </View> : null}
    {showControls ? <Text style={s.hint}>{layout === 'wrap' ? 'Fits text on this phone. For tables or screen layouts, use Exact grid.' : layout === 'grid' ? 'Keeps terminal columns. Swipe sideways for wide output.' : 'Shrinks the terminal grid to fit this phone.'}</Text> : null}
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
    {!following ? <Pressable accessibilityRole="button" accessibilityLabel="Jump to latest terminal output" style={s.latest} onPress={() => {
      atEnd.current = true; userScrolling.current = false; setFollowing(true); followLatest();
    }}><Text style={s.latestText}>↓ Jump to latest</Text></Pressable> : null}
    <ScrollView ref={ref} testID="terminal-output" style={s.term} onScroll={onScroll} scrollEventThrottle={100}
      onScrollBeginDrag={() => { userScrolling.current = true; }}
      onScrollEndDrag={e => { onScroll(e); userScrolling.current = false; }}
      onMomentumScrollBegin={() => { userScrolling.current = true; }}
      onMomentumScrollEnd={e => { onScroll(e); userScrolling.current = false; }}
      onLayout={e => { const { width, height } = e.nativeEvent.layout; setViewport(previous => previous.width === width && previous.height === height ? previous : { width, height }); followLatest(); }}
    onContentSizeChange={followLatest}>
    {layout === 'wrap' ? <View style={s.content}>
      {contents}
    </View> : <ScrollView horizontal showsHorizontalScrollIndicator contentContainerStyle={[s.content, width ? { minWidth: width } : null]}>
      {contents}
    </ScrollView>}
  </ScrollView></View>;
}
export const terminalSizes = sizes.length;

const s = StyleSheet.create({
  term: { flex: 1, backgroundColor: c.term, overflow: 'hidden' },
  latest: { alignSelf: 'flex-end', paddingHorizontal: 12, paddingVertical: 8 },
  latestText: { color: c.accent, fontSize: 12 },
  tools: { flexDirection: 'row', flexWrap: 'wrap', alignItems: 'center', gap: 6, paddingHorizontal: 12, paddingVertical: 6 },
  tool: { minHeight: 34, paddingHorizontal: 10, justifyContent: 'center', borderRadius: 8 },
  toolOn: { backgroundColor: c.raised },
  toolText: { color: c.muted, fontSize: 12, fontWeight: '600' },
  hint: { paddingHorizontal: 12, paddingBottom: 6, color: c.muted, fontSize: 11, lineHeight: 15 },
  dimensions: { color: c.faint, fontSize: 11, marginLeft: 'auto' },
  measure: { position: 'absolute', opacity: 0, left: 0, top: 0, width: 10000 },
  content: { padding: 12, flexShrink: 0 },
  text: { flexShrink: 0, fontFamily: mono, color: c.termText },
});
