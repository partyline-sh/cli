import { router } from 'expo-router';
import { Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { usePartyline, type Computer, type Session } from '../runtime';
import { storePreview } from '../preview/config';
import { Avatar, Banner, Empty, Group, LargeTitle, List, Row, StatePill } from './parts';
import { paneToRoute } from './route';
import { age, c } from './theme';

const connectionLabel: Record<Computer['connection'], [string, string]> = {
  connected: ['● connected', c.ok], connecting: ['connecting…', c.warn], reconnecting: ['reconnecting…', c.warn],
  offline: ['offline', c.faint], revoked: ['pair again', c.accent],
};

export function openSession(s: Pick<Session, 'computerId' | 'pane'>) {
  router.push({ pathname: '/session/[computer]/[pane]', params: { computer: s.computerId, pane: paneToRoute(s.pane) } });
}

function SessionRow({ s, last }: { s: Session; last?: boolean }) {
  return <Row last={last} left={<Avatar engine={s.engine} />} title={s.title} sub={s.lastLine} onPress={() => openSession(s)}
    label={`${s.title}, ${s.engine}, ${s.state === 'waiting' ? 'waiting on you' : s.state}`}
    right={<View style={st.side}><Text style={st.time}>{age(s.updatedAt)}</Text><StatePill state={s.state} /></View>} />;
}

/** Every session on every paired computer; whoever is waiting on you first, then one group per computer. */
export function SessionsScreen() {
  const app = usePartyline();
  const waiting = app.sessions.filter(s => s.state === 'waiting');
  const down = app.computers.filter(k => k.connection === 'reconnecting' || k.connection === 'connecting');
  const revoked = app.computers.filter(k => k.connection === 'revoked');
  return <SafeAreaView edges={['top']} style={st.screen}>
    <LargeTitle right={<Pressable accessibilityRole="button" accessibilityLabel="Pair a computer" style={st.icon} onPress={() => router.push('/pair')}><Text style={st.iconText}>+</Text></Pressable>}>Sessions</LargeTitle>
    {app.mode === 'fixture' && !storePreview ? <Text style={st.fixture}>Example data: no computer is connected.</Text> : null}
    {down.map(k => <Banner key={k.id} busy>{`Reconnecting to ${k.name}…`}</Banner>)}
    {revoked.map(k => <Banner key={k.id}>{`${k.name} no longer knows this phone. Pair again from Computers.`}</Banner>)}
    <ScrollView contentContainerStyle={st.scroll}>
      {app.computers.length === 0 ? <Empty>{'No computer paired yet. In partyline on your computer press ctrl-\\ then p, then tap + here to scan the code.'}</Empty> : null}
      {waiting.length > 0 ? <><Group title="Waiting on you" /><List>{waiting.map((s, i) => <SessionRow key={s.id} s={s} last={i === waiting.length - 1} />)}</List></> : null}
      {app.computers.map(k => {
        const rows = app.sessions.filter(s => s.computerId === k.id && s.state !== 'waiting');
        const [label, colour] = connectionLabel[k.connection];
        return <View key={k.id}>
          <Group title={k.name} right={label} rightColour={colour} />
          {rows.length > 0 ? <List>{rows.map((s, i) => <SessionRow key={s.id} s={s} last={i === rows.length - 1} />)}</List>
            : <Text style={st.none}>{k.connection === 'connected' ? 'No other sessions.' : 'Sessions show when it connects.'}</Text>}
        </View>;
      })}
    </ScrollView>
  </SafeAreaView>;
}

const st = StyleSheet.create({
  screen: { flex: 1, backgroundColor: c.ground },
  scroll: { paddingHorizontal: 14, paddingBottom: 16 },
  side: { alignItems: 'flex-end', gap: 4 },
  time: { color: c.faint, fontSize: 12, fontVariant: ['tabular-nums'] },
  icon: { width: 36, height: 36, borderRadius: 18, backgroundColor: c.raised, alignItems: 'center', justifyContent: 'center' },
  iconText: { color: c.text, fontSize: 22, lineHeight: 24 },
  fixture: { color: c.faint, fontSize: 12, marginHorizontal: 18, marginBottom: 6 },
  none: { color: c.faint, fontSize: 13, marginHorizontal: 6 },
});
