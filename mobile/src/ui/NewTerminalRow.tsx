import { useEffect, useRef, useState } from 'react';
import { Alert, Platform, Pressable, Text, View } from 'react-native';
import { usePartyline, type Computer } from '../runtime';
import { router } from 'expo-router';
import { paneToRoute } from './route';
import { ControlDelivery } from './ControlDelivery';
import { c } from './theme';

/** New terminals use the negotiated byte stream; older hosts expose an explicit legacy option. */
export function NewTerminalRow({ computer }: { computer: Computer }) {
  const app = usePartyline();
  const [request, setRequest] = useState<string>();
  const opened = useRef<string | undefined>(undefined);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const delivery = [...app.deliveries].reverse().find(d => d.computerId === computer.id && d.pane === 'shell');
  const streaming=Platform.OS!=='web'&&computer.capabilities.includes('term_stream')&&!!app.openTerminalStream;
  const blocked=busy||(!streaming&&(delivery?.status==='waiting'||delivery?.status==='unknown'));
  const supported = streaming||computer.capabilities.includes('shell_open');
  useEffect(() => {
    if (request && opened.current !== request && delivery?.id === request && delivery.status === 'confirmed') {
      const pane = (delivery.data as { pane?: unknown } | undefined)?.pane;
      opened.current = request;
      if (typeof pane === 'string' && /^%\d+$/.test(pane)) {
        app.selectView('terminal');
        router.push({ pathname: '/session/[computer]/[pane]', params: { computer: computer.id, pane: paneToRoute(pane) } });
      }
    }
  }, [request, delivery, app, computer.id]);
  return <View style={{ gap: 10, padding: 14, marginTop: 8, backgroundColor: c.surface, borderRadius: 12 }}>
    <Pressable accessibilityRole="button" accessibilityLabel={`New terminal on ${computer.name}`} disabled={blocked || !supported || computer.connection !== 'connected'} onPress={() => {
      if(streaming){router.push({pathname:'/terminal/[computer]/[session]',params:{computer:computer.id,session:'new'}});return;}
      setBusy(true); setError('');
      void app.openShell(computer.id).then(d => setRequest(d.id)).catch(e => setError(String(e))).finally(() => setBusy(false));
    }}><Text style={{ color: blocked || !supported || computer.connection !== 'connected' ? c.faint : c.accent, fontSize: 15 }}>＋ New terminal{!streaming?' · legacy':''}</Text></Pressable>
    <Text style={{ color: c.muted, fontSize: 12 }}>{supported ? `Opens a shell in your home directory on ${computer.name}. It keeps running when you leave the phone.` : 'Update Partyline on this computer to open a terminal.'}</Text>
    {(app.phoneTerminals||[]).filter(t=>t.computerId===computer.id).map(t=><View key={t.id} style={{ gap: 6 }}>
      <Pressable accessibilityRole="button" style={{ minHeight: 44, justifyContent: 'center' }} onPress={()=>router.push({pathname:'/terminal/[computer]/[session]',params:{computer:computer.id,session:t.id}})}><Text style={{color:c.text}}>{t.title} · Reconnect</Text><Text style={{color:c.muted,fontSize:12}}>Detached terminals expire after 24 hours.</Text></Pressable>
      {app.endPhoneTerminal ? <Pressable accessibilityRole="button" accessibilityLabel={`Close terminal ${t.title}`} disabled={busy || computer.connection !== 'connected'} style={{ minHeight: 44, justifyContent: 'center' }} onPress={() => Alert.alert('Close terminal?', `This ends ${t.title} and its shell process. Detached background jobs may continue.`, [{ text: 'Cancel', style: 'cancel' }, { text: 'Close terminal', style: 'destructive', onPress: () => {
        const end = app.endPhoneTerminal; if (!end) return;
        setBusy(true); setError(''); void end(computer.id, t.id).catch(e => setError(String(e))).finally(() => setBusy(false));
      } }])}><Text style={{ color: c.accent }}>Close terminal</Text></Pressable> : null}
    </View>)}
    {error ? <Text style={{ color: c.warn }}>{error}</Text> : null}
    {!streaming ? <ControlDelivery delivery={delivery} /> : null}
  </View>;
}
