import { memo, useMemo, type ReactNode } from 'react';
import { ScrollView, StyleSheet, Text, View } from 'react-native';
import { Lexer, type Token, type Tokens } from 'marked';
import { c, mono } from './theme';

/** Parse once per changed message; no HTML renderer, WebView, remote images or link execution. */
export const ChatMarkdown = memo(function ChatMarkdown({ text }: { text: string }) {
  const tokens = useMemo(() => {
    if (text.length > 32768) return;
    try { return Lexer.lex(text, { gfm: true, breaks: true }); } catch { return; }
  }, [text]);
  return <View style={s.message}>{tokens ? blocks(tokens) : <Text selectable style={s.text}>{text}</Text>}</View>;
});

function inline(tokens: Token[] | undefined, depth = 0): ReactNode {
  return tokens?.map((token, i) => {
    if (depth > 8) return token.raw;
    const children = 'tokens' in token ? inline(token.tokens, depth + 1) : 'text' in token ? token.text : token.raw;
    switch (token.type) {
      case 'strong': return <Text key={i} style={s.strong}>{children}</Text>;
      case 'em': return <Text key={i} style={s.em}>{children}</Text>;
      case 'del': return <Text key={i} style={s.del}>{children}</Text>;
      case 'codespan': return <Text key={i} style={s.code}>{token.text}</Text>;
      case 'br': return '\n';
      case 'link': return <Text key={i}>{children}{token.href !== token.text ? ` (${token.href})` : ''}</Text>;
      case 'image': return `[image: ${token.text || token.href}]`;
      case 'html': return token.raw;
      default: return children;
    }
  });
}

function blocks(tokens: Token[], depth = 0): ReactNode {
  return tokens.map((token, i) => {
    if (depth > 8) return <Text selectable key={i} style={s.text}>{token.raw}</Text>;
    switch (token.type) {
      case 'space': return null;
      case 'heading': return <Text selectable accessibilityRole="header" key={i} style={[s.text, s.strong]}>{inline(token.tokens)}</Text>;
      case 'paragraph': case 'text': return <Text selectable key={i} style={s.text}>{inline(token.tokens) ?? token.text}</Text>;
      case 'code': return <ScrollView horizontal key={i} style={s.codeBlock} showsHorizontalScrollIndicator><Text selectable style={[s.text, s.codeBlockText]}>{token.text}</Text></ScrollView>;
      case 'list': return <View key={i} style={s.list}>{token.items.map((item: Tokens.ListItem, index: number) => <View key={index} style={s.listItem}>
        <Text style={s.bullet}>{token.ordered ? `${Number(token.start) + index}.` : item.task ? item.checked ? '☑' : '☐' : '•'}</Text>
        <View style={s.listBody}>{blocks(item.tokens, depth + 1)}</View>
      </View>)}</View>;
      case 'blockquote': return <View key={i} style={s.quote}>{blocks(token.tokens ?? [], depth + 1)}</View>;
      case 'hr': return <View key={i} style={s.rule} />;
      case 'table': return <MarkdownTable key={i} token={token as Tokens.Table} />;
      default: return <Text selectable key={i} style={s.text}>{token.raw}</Text>;
    }
  });
}

function MarkdownTable({ token }: { token: Tokens.Table }) {
  if (token.header.length > 12) return <Text selectable style={s.text}>{token.raw}</Text>;
  const rows = [token.header, ...token.rows];
  const widths = token.header.map((_, col) => Math.min(180, Math.max(90, ...rows.map(row => (row[col]?.text.length ?? 0) * 7 + 18))));
  return <ScrollView horizontal style={s.tableScroll} showsHorizontalScrollIndicator><View style={s.table}>
    {rows.map((row, index) => <View key={index} style={[s.tableRow, index === 0 && s.tableHead]}>{row.map((cell, col) => <Text selectable key={col} accessibilityRole={index === 0 ? 'header' : undefined} style={[s.text, s.cell, { width: widths[col], textAlign: token.align[col] ?? 'left' }, index === 0 && s.strong]}>{inline(cell.tokens)}</Text>)}</View>)}
  </View></ScrollView>;
}
const s = StyleSheet.create({
  message: { gap: 7, marginTop: 3, maxWidth: '100%' }, text: { color: c.text, fontSize: 15, lineHeight: 22, flexShrink: 1 },
  strong: { fontWeight: '700' }, em: { fontStyle: 'italic' }, del: { textDecorationLine: 'line-through' },
  code: { fontFamily: mono, fontSize: 13, color: c.claude, backgroundColor: c.raised },
  codeBlock: { backgroundColor: c.term, borderRadius: 7, maxWidth: '100%' }, codeBlockText: { fontFamily: mono, fontSize: 13, lineHeight: 20, padding: 10, color: c.termText },
  list: { gap: 4 }, listItem: { flexDirection: 'row', gap: 7 }, bullet: { color: c.muted, fontSize: 15, lineHeight: 22, minWidth: 16 }, listBody: { flex: 1, minWidth: 0, gap: 5 },
  quote: { borderLeftWidth: 2, borderLeftColor: c.accent, paddingLeft: 10, gap: 5 }, rule: { height: 1, backgroundColor: c.line, marginVertical: 3 },
  tableScroll: { maxWidth: '100%' }, table: { borderWidth: 1, borderColor: c.line, borderRadius: 6, overflow: 'hidden' }, tableRow: { flexDirection: 'row' }, tableHead: { backgroundColor: c.raised },
  cell: { padding: 8, borderRightWidth: StyleSheet.hairlineWidth, borderBottomWidth: StyleSheet.hairlineWidth, borderColor: c.line },
});
