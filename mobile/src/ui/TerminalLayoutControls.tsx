import { useIsFocused } from 'expo-router';
import { useEffect, useRef, useState } from 'react';
import { Pressable, StyleSheet, Text, View } from 'react-native';
import { usePartyline } from '../runtime';
import type { TerminalViewport } from './terminal-layout';
import { c } from './theme';

export function TerminalLayoutControls({ computer, pane, viewport }: { computer: string; pane: string; viewport?: TerminalViewport }) {
  const app = usePartyline(), focused = useIsFocused();
  const host = app.computers.find(k => k.id === computer);
  const supported = !!host?.capabilities.includes('terminal_resize');
  const resize = app.resizeTerminal;
  const [phone, setPhone] = useState(false);
  const [error, setError] = useState('');
  const [pending, setPending] = useState(false);
  const engaged = useRef(false);

  // Release when hidden, including a different session pushed over this one.
  useEffect(() => () => {
    if (!engaged.current) return;
    engaged.current = false;
    void resize(computer, pane, 'desktop').catch(() => {});
  }, [focused, computer, pane, resize]);
  useEffect(() => {
    if (!phone || !focused || !viewport) return;
    let cancelled = false;
    const apply = () => {
      engaged.current = true;
      void resize(computer, pane, 'phone', viewport).then(() => {
        if (!cancelled) setPending(false);
      }).catch((e: unknown) => {
        if (!cancelled) { setError(e instanceof Error ? e.message : String(e)); setPhone(false); setPending(false); }
      });
    };
    const initial = setTimeout(apply, 250); // coalesce keyboard/rotation layout changes
    const renewal = setInterval(apply, 10000);
    return () => { cancelled = true; clearTimeout(initial); clearInterval(renewal); };
  }, [phone, focused, viewport?.columns, viewport?.rows, host?.connection, computer, pane, resize]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (phone || !engaged.current) return;
    engaged.current = false;
    void resize(computer, pane, 'desktop').catch((e: unknown) => setError(`${e instanceof Error ? e.message : String(e)}. Desktop layout was not confirmed.`));
  }, [phone, computer, pane, resize]);

  const desktop = () => {
    setError(''); setPhone(false); setPending(false);
  };
  return <View style={s.wrap}>
    <View style={s.row}>
      <Pressable accessibilityRole="radio" accessibilityLabel="Keep computer terminal size" accessibilityState={{ checked: !phone }} onPress={desktop} style={[s.mode, !phone && s.on]}>
        <Text style={[s.label, !phone && s.active]}>Computer size</Text>
      </Pressable>
      <Pressable accessibilityRole="radio" accessibilityLabel="Resize the shared terminal session" accessibilityState={{ checked: phone, disabled: !supported || !viewport || host?.connection !== 'connected' }}
        disabled={!supported || !viewport || host?.connection !== 'connected'} onPress={() => { if (phone) return; setError(''); setPending(true); setPhone(true); }} style={[s.mode, phone && s.on, !supported && s.disabled]}>
        <Text style={[s.label, phone && s.active]}>Resize shared session</Text>
      </Pressable>
      {phone && viewport ? <Text style={s.size}>{pending ? 'Applying…' : `${viewport.columns} × ${viewport.rows}`}</Text> : null}
    </View>
    <Text style={s.hint}>{supported ? 'Optional: changes this session on the computer too. Leaving restores it, or within 30 seconds if disconnected. Wrap text only changes this phone.' : 'Update Partyline on the computer to resize the shared session.'}</Text>
    {error ? <Text accessibilityLiveRegion="assertive" style={s.error}>{error}</Text> : null}
  </View>;
}
const s = StyleSheet.create({
  wrap: { paddingHorizontal: 12, paddingTop: 6, gap: 5, backgroundColor: c.term },
  row: { flexDirection: 'row', flexWrap: 'wrap', alignItems: 'center', gap: 6 },
  mode: { minHeight: 36, justifyContent: 'center', paddingHorizontal: 12, borderRadius: 8 },
  on: { backgroundColor: c.raised }, disabled: { opacity: 0.5 },
  label: { color: c.muted, fontSize: 12, fontWeight: '600' }, active: { color: c.text },
  size: { color: c.faint, fontSize: 11, marginLeft: 'auto' },
  hint: { color: c.muted, fontSize: 11, lineHeight: 15 }, error: { color: c.warn, fontSize: 12 },
});
