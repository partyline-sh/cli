import { CameraView, useCameraPermissions } from 'expo-camera';
import { router } from 'expo-router';
import { useEffect, useRef, useState } from 'react';
import { ActivityIndicator, Keyboard, KeyboardAvoidingView, Linking, Platform, Pressable, ScrollView, StyleSheet, Text, TextInput, View, type ScrollViewInstance } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { usePartyline } from '../runtime';
import { c, mono } from './theme';

/** Pairing: scan the code partyline shows, check the four digits match, approve on the computer. */
export function PairScreen() {
  const app = usePartyline();
  const [permission, requestPermission] = useCameraPermissions();
  const [link, setLink] = useState('');
  // The camera reports the same code many times a second. A code is tried once: after it fails it
  // stays tried until the person asks to scan again or shows a different code.
  const scroll = useRef<ScrollViewInstance>(null);
  const busy = useRef(false);
  const tried = useRef('');
  const p = app.pairing;

  useEffect(() => {
    const shown = Keyboard.addListener('keyboardDidShow', () => scroll.current?.scrollToEnd({ animated: true }));
    return () => shown.remove();
  }, []);
  useEffect(() => { app.startScan(); return () => app.cancelPair(); }, []); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { if (p.status === 'paired') { const t = setTimeout(() => router.back(), 1200); return () => clearTimeout(t); } }, [p.status]);

  const submit = (value: string, fromCamera: boolean) => {
    const v = value.trim();
    if (busy.current || !v || (fromCamera && v === tried.current)) return;
    Keyboard.dismiss();
    busy.current = true;
    tried.current = v;
    // A failure also lands in app.pairing as an error, which the screen shows.
    app.pair(v).catch(() => {}).finally(() => { busy.current = false; });
  };
  const scanAgain = () => { tried.current = ''; app.startScan(); };
  const close = () => { app.cancelPair(); router.back(); };
  const name = p.status === 'paired' ? app.computers.find(k => k.id === p.computerId)?.name : undefined;

  return <SafeAreaView style={s.screen}>
    <View style={s.top}><Text accessibilityRole="header" style={s.heading}>Pair a computer</Text>
      <Pressable accessibilityRole="button" accessibilityLabel="Close" hitSlop={12} onPress={close}><Text style={s.close}>Cancel</Text></Pressable></View>

    <KeyboardAvoidingView style={s.form} behavior={Platform.OS === 'ios' ? 'padding' : 'height'}>
    <ScrollView ref={scroll} keyboardShouldPersistTaps="handled" contentContainerStyle={s.formContent}
      onLayout={() => { if (Keyboard.isVisible()) scroll.current?.scrollToEnd({ animated: true }); }}>
    {p.status === 'awaiting-approval' ? <View style={s.center}>
      <Text style={s.lead}>Check this matches the code on your computer, then press ⏎ there.</Text>
      <Text accessibilityLabel={`Code ${p.code.split('').join(' ')}`} style={s.code}>{p.code}</Text>
      <Text style={s.fine}>{`This phone's key ${p.fingerprint}`}</Text>
      <ActivityIndicator color={c.muted} />
    </View>
    : p.status === 'connecting' ? <View style={s.center}><ActivityIndicator color={c.muted} /><Text style={s.lead}>Connecting to the computer…</Text></View>
    : p.status === 'paired' ? <View style={s.center}><Text style={s.done}>✓</Text><Text style={s.lead}>{`Paired with ${name ?? 'your computer'}.`}</Text></View>
    : <>
      <View style={s.cameraBox}>
        {permission?.granted ? <CameraView style={StyleSheet.absoluteFill} facing="back" barcodeScannerSettings={{ barcodeTypes: ['qr'] }}
          onBarcodeScanned={r => submit(r.data, true)} />
          : <View style={s.center}>
            <Text style={s.lead}>{permission?.canAskAgain === false ? 'Camera access is off for Partyline.' : 'Partyline uses the camera to scan the pairing code.'}</Text>
            <Pressable accessibilityRole="button" style={s.button} onPress={() => (permission?.canAskAgain === false ? Linking.openSettings() : requestPermission())}>
              <Text style={s.buttonText}>{permission?.canAskAgain === false ? 'Open Settings' : 'Allow the camera'}</Text></Pressable>
          </View>}
        {permission?.granted ? <View pointerEvents="none" style={s.guide} /> : null}
      </View>
      <Text style={s.lead}>{'On your computer, in partyline, press ctrl-\\ then p, and point the camera at the code.'}</Text>
      {p.status === 'error' ? <Text accessibilityLiveRegion="assertive" style={s.error}>{p.message}</Text> : null}
      {p.status === 'error' && p.code !== 'camera-denied' ? <Pressable accessibilityRole="button" onPress={scanAgain}><Text style={s.link}>Scan again</Text></Pressable> : null}
      {p.status === 'error' && p.code === 'camera-denied' ? <Pressable accessibilityRole="button" onPress={() => Linking.openSettings()}><Text style={s.link}>Open Settings</Text></Pressable> : null}
      <View style={s.paste}>
        <TextInput value={link} onChangeText={setLink} placeholder="Or paste the pairing link" placeholderTextColor={c.faint} autoCapitalize="none" autoCorrect={false}
          style={s.input} onSubmitEditing={() => submit(link, false)} accessibilityLabel="Pairing link" />
        <Pressable accessibilityRole="button" disabled={!link.trim()} style={[s.button, !link.trim() && { opacity: 0.4 }]} onPress={() => submit(link, false)}><Text style={s.buttonText}>Pair</Text></Pressable>
      </View>
    </>}
    </ScrollView>
    </KeyboardAvoidingView>
  </SafeAreaView>;
}

const s = StyleSheet.create({
  screen: { flex: 1, backgroundColor: c.ground, paddingHorizontal: 20, gap: 16 },
  form: { flex: 1 },
  formContent: { flexGrow: 1, gap: 16, paddingBottom: 20 },
  top: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', paddingTop: 8 },
  heading: { color: c.text, fontSize: 22, fontWeight: '700' },
  close: { color: c.accent, fontSize: 16 },
  center: { flex: 1, alignItems: 'center', justifyContent: 'center', gap: 16, padding: 16 },
  cameraBox: { aspectRatio: 1, width: '100%', borderRadius: 20, overflow: 'hidden', backgroundColor: c.surface },
  guide: { position: 'absolute', top: '18%', left: '18%', right: '18%', bottom: '18%', borderWidth: 3, borderColor: c.accent, borderRadius: 16 },
  lead: { color: c.muted, fontSize: 15, lineHeight: 21, textAlign: 'center' },
  code: { color: c.text, fontFamily: mono, fontSize: 56, fontWeight: '700', letterSpacing: 12 },
  fine: { color: c.faint, fontFamily: mono, fontSize: 12 },
  done: { color: c.ok, fontSize: 56 },
  error: { color: c.warn, fontSize: 14, textAlign: 'center' },
  link: { color: c.accent, fontSize: 15, textAlign: 'center' },
  paste: { flexDirection: 'row', gap: 8, alignItems: 'center' },
  input: { flex: 1, minHeight: 44, borderRadius: 12, backgroundColor: c.surface, color: c.text, paddingHorizontal: 12, fontSize: 15 },
  button: { backgroundColor: c.accent, borderRadius: 12, paddingHorizontal: 16, minHeight: 44, justifyContent: 'center' },
  buttonText: { color: c.ground, fontSize: 15, fontWeight: '700' },
});
