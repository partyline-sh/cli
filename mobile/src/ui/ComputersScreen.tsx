import { useState } from 'react';
import { router } from 'expo-router';
import { Alert, Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { usePartyline, type Computer } from '../runtime';
import { storePreview } from '../preview/config';
import { ComputerDetails } from './ComputerDetails';
import { computerGroups, computerStatus } from './computer-groups';
import { Avatar, Group, LargeTitle, List, Row } from './parts';
import { c } from './theme';

/** Only computers paired with this phone, grouped by their actual relay route. */
export function ComputersScreen() {
  const app = usePartyline();
  const [closed, setClosed] = useState<ReadonlySet<string>>(() => new Set());
  const [selected, setSelected] = useState<string>();
  const groups = computerGroups(app.computers);
  const forget = (computer: Computer) => Alert.alert(`Forget ${computer.name}?`,
    `This phone deletes its keys and stops seeing ${computer.name}'s sessions. ${computer.name} still lists this phone until you remove it there: run ptln remote phones, then ptln remote revoke <id>.`,
    [{ text: 'Cancel', style: 'cancel' }, { text: 'Forget', style: 'destructive', onPress: () => {
      app.forgetComputer(computer.id).catch((error: unknown) => Alert.alert(`Could not forget ${computer.name}`, error instanceof Error ? error.message : String(error)));
    } }]);
  const toggle = (id: string) => setClosed(current => {
    const next = new Set(current);
    if (next.has(id)) next.delete(id); else next.add(id);
    return next;
  });
  return <SafeAreaView edges={['top']} style={s.screen}>
    <LargeTitle>Computers</LargeTitle>
    {app.mode === 'fixture' && !storePreview ? <Text style={s.hint}>Example data: no computer is connected.</Text> : null}
    <ScrollView contentContainerStyle={s.scroll}>
      {groups.map(group => <View key={group.id} style={s.gateway}>
        <Pressable onPress={() => toggle(group.id)} accessibilityRole="button"
          accessibilityLabel={`${group.title}${group.address ? `, ${group.address}` : ''}, ${group.connected} of ${group.computers.length} computers connected`}
          accessibilityState={{ expanded: !closed.has(group.id) }} style={s.header}>
          <View style={s.headerMain}>
            <Text style={s.gatewayTitle}>{group.title}</Text>
            {group.address ? <Text selectable style={s.address}>{group.address}</Text> : null}
            <Text style={s.count}>{group.connected} connected · {group.computers.length} paired</Text>
          </View>
          <Text style={s.chevron}>{closed.has(group.id) ? '+' : '−'}</Text>
        </Pressable>
        {!closed.has(group.id) ? <List>{group.computers.map((computer, index) => {
          const sessions = app.sessions.filter(item => item.computerId === computer.id).length;
          const label = computerStatus(computer);
          const online = computer.connection === 'connected';
          const expanded = selected === computer.id;
          const sub = online ? `${sessions} session${sessions === 1 ? '' : 's'}`
            : computer.connection === 'revoked' ? 'Pair this phone again to reconnect.'
            : 'Tap for connection details.';
          return <View key={computer.id}>
            <Row last={expanded || index === group.computers.length - 1} left={<Avatar engine="computer" label="⌘" />} title={computer.name}
              sub={sub} label={`${computer.name}, ${label}. ${expanded ? 'Hide' : 'Show'} connection details.`}
              onPress={() => setSelected(expanded ? undefined : computer.id)}
              right={<View style={[s.pill, online && s.online]}><Text style={[s.pillText, online && s.onlineText]}>{label}</Text></View>} />
            {expanded ? <ComputerDetails computer={computer} sessions={sessions}
              onReconnect={() => app.reconnect(computer.id)} onPair={() => router.push('/pair')} onForget={() => forget(computer)} /> : null}
          </View>;
        })}</List> : null}
      </View>)}
      {groups.length ? <Text style={s.hint}>Only computers paired with this phone appear here. Gateway membership and phone pairing are separate approvals.</Text>
        : <Text style={s.empty}>Pair a computer to see its sessions and relay route here.</Text>}
      <Group title="Add a computer" />
      <List><Row last left={<Avatar engine="claude" label="+" />} title="Scan a pairing code" sub={'On the computer: ctrl-\\ then p'} onPress={() => router.push('/pair')} /></List>
    </ScrollView>
  </SafeAreaView>;
}

const s = StyleSheet.create({
  screen: { flex: 1, backgroundColor: c.ground },
  scroll: { paddingHorizontal: 14, paddingBottom: 24 },
  gateway: { marginBottom: 14 },
  header: { paddingHorizontal: 8, paddingVertical: 12, flexDirection: 'row', alignItems: 'center', gap: 12 },
  headerMain: { flex: 1, gap: 4 },
  gatewayTitle: { color: c.text, fontSize: 17, fontWeight: '700' },
  address: { color: c.muted, fontSize: 13, lineHeight: 18 },
  count: { color: c.faint, fontSize: 12 },
  chevron: { color: c.muted, fontSize: 23, paddingHorizontal: 8 },
  pill: { borderRadius: 999, paddingHorizontal: 8, paddingVertical: 5, backgroundColor: c.raised },
  online: { backgroundColor: 'rgba(74,222,128,0.16)' },
  pillText: { color: c.muted, fontSize: 11, fontWeight: '600' },
  onlineText: { color: c.ok },
  hint: { color: c.faint, fontSize: 12, lineHeight: 18, marginHorizontal: 6, marginTop: 2 },
  empty: { color: c.muted, fontSize: 15, lineHeight: 21, paddingVertical: 24, paddingHorizontal: 8 },
});
