import { useFocusEffect } from 'expo-router';
import { useCallback, useRef, useState } from 'react';
import { Image, Keyboard, Modal, Platform, Pressable, StyleSheet, Text, View } from 'react-native';
import * as DocumentPicker from 'expo-document-picker';
import * as ImagePicker from 'expo-image-picker';
import { File } from 'expo-file-system';
import { usePartyline } from '../runtime';
import { MAX_ATTACHMENT_BYTES } from '../runtime/uploads';
import { c } from './theme';

/** The OS chooses the source; Partyline transfers it, then adds a path to the draft. */
export function AttachmentPicker({ computerId, pane, onBusy }: {
  computerId: string; pane: string; onBusy: (busy: boolean) => void;
}) {
  const app = usePartyline();
  const computer = app.computers.find(k => k.id === computerId);
  const supported = computer?.capabilities.includes('uploads');
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState<number>();
  const [message, setMessage] = useState('');
  const [preview, setPreview] = useState<string>();
  const controller = useRef<AbortController | undefined>(undefined);
  const nextPicker = useRef<'photo' | 'file' | undefined>(undefined);
  useFocusEffect(useCallback(() => () => { nextPicker.current = undefined; controller.current?.abort(); }, []));

  const pick = async (kind: 'photo' | 'file') => {
    if (controller.current) return;
    setOpen(false);
    setMessage('');
    setPreview(undefined);
    const abort = new AbortController();
    controller.current = abort;
    setBusy(true);
    onBusy(true);
    try {
      let uri: string, name: string, mime: string, size: number | undefined, webFile: globalThis.File | undefined;
      if (kind === 'photo') {
        const result = await ImagePicker.launchImageLibraryAsync({ mediaTypes: ['images'], allowsEditing: false });
        if (result.canceled) return;
        const asset = result.assets[0];
        uri = asset.uri; name = asset.fileName || 'photo.jpg'; mime = asset.mimeType || 'application/octet-stream';
        size = asset.fileSize; webFile = asset.file;
        setPreview(uri);
      } else {
        const result = await DocumentPicker.getDocumentAsync({ copyToCacheDirectory: true, multiple: false, base64: false });
        if (result.canceled) return;
        const asset = result.assets[0];
        uri = asset.uri; name = asset.name; mime = asset.mimeType || 'application/octet-stream'; size = asset.size; webFile = asset.file;
      }
      if (abort.signal.aborted) return;
      name = name.replace(/[\x00-\x1f\x7f]/g, '').slice(0, 256) || 'attachment';
      const nativeFile = Platform.OS === 'web' ? undefined : new File(uri);
      const actualSize = nativeFile?.size ?? webFile?.size ?? size;
      if (actualSize === 0) throw new Error('Choose a non-empty file.');
      if (actualSize === undefined || actualSize > MAX_ATTACHMENT_BYTES)
        throw new Error('Choose a file of 20 MiB or less.');
      setProgress(0);
      setMessage(`Uploading ${name.replace(/[\x00-\x1f\x7f]/g, '')}`);
      const bytes = nativeFile ? await nativeFile.bytes() : new Uint8Array(await webFile!.arrayBuffer());
      if (abort.signal.aborted) return;
      await app.uploadAttachment(computerId, pane, { name, mime, bytes }, setProgress, abort.signal);
      setMessage('Attached to your draft. Press Send when ready.');
    } catch (e) {
      if (!abort.signal.aborted) setMessage(e instanceof Error ? e.message : String(e));
    } finally {
      if (controller.current === abort) {
        controller.current = undefined;
        if (abort.signal.aborted) setMessage('Upload cancelled');
        setProgress(undefined);
        setBusy(false);
        onBusy(false);
      }
    }
  };
  const choose = (kind: 'photo' | 'file') => {
    setOpen(false);
    // UIKit cannot present a picker while our sheet is still dismissing.
    if (Platform.OS === 'ios') nextPicker.current = kind;
    else void pick(kind); // Web also needs the original user activation.
  };
  const sheetDismissed = () => {
    const kind = nextPicker.current;
    nextPicker.current = undefined;
    if (kind) void pick(kind);
  };
  return <View>
    <Pressable accessibilityRole="button" accessibilityLabel="Attach a photo or file" disabled={busy}
      style={[s.attach, busy && { opacity: 0.4 }]} onPress={() => { Keyboard.dismiss(); setOpen(true); }}>
      <Text style={s.link}>＋ Attach</Text>
    </Pressable>
    {message ? <View style={s.status} accessibilityLiveRegion="polite">
      {preview ? <Image source={{ uri: preview }} style={s.preview} accessibilityLabel="Selected photo" /> : null}
      <View style={s.detail}><Text style={s.text}>{message}</Text>
        {progress !== undefined ? <View accessibilityRole="progressbar" accessibilityValue={{ min: 0, max: 100, now: Math.round(progress * 100) }} style={s.track}>
          <View style={[s.fill, { width: `${Math.round(progress * 100)}%` }]} />
        </View> : null}
      </View>
      {busy ? <Pressable accessibilityRole="button" onPress={() => { controller.current?.abort(); setMessage('Upload cancelled'); }}><Text style={s.link}>Cancel</Text></Pressable> : null}
    </View> : null}
    <Modal visible={open} transparent animationType="fade" onRequestClose={() => setOpen(false)} onDismiss={sheetDismissed}>
      <View style={s.backdrop}><View style={s.sheet} accessibilityViewIsModal>
        <Text style={s.title}>Attach to your draft</Text>
        <Text style={s.text}>Up to 20 MiB per file. Files go to {computer?.name || 'your computer'} over your encrypted connection.</Text>
        <Text style={s.text}>This computer caches up to 200 MiB per phone and removes older files when space is needed. A confirmed upload adds a path to your draft; you still press Send.</Text>
        {supported && computer?.connection === 'connected' ? <>
          <Pressable accessibilityRole="button" style={s.choice} onPress={() => choose('photo')}><Text style={s.choiceText}>Choose a photo</Text></Pressable>
          <Pressable accessibilityRole="button" style={s.choice} onPress={() => choose('file')}><Text style={s.choiceText}>Choose a file</Text></Pressable>
        </> : <Text style={s.notice}>{supported ? 'Reconnect your computer to attach files.' : 'Update Partyline on your computer to enable attachments.'}</Text>}
        <Pressable accessibilityRole="button" style={s.choice} onPress={() => setOpen(false)}><Text style={s.link}>Cancel</Text></Pressable>
      </View></View>
    </Modal>
  </View>;
}

const s = StyleSheet.create({
  attach: { alignSelf: 'flex-start', minHeight: 36, justifyContent: 'center', paddingHorizontal: 14 },
  link: { color: c.accent, fontSize: 14, fontWeight: '600' },
  status: { flexDirection: 'row', alignItems: 'center', gap: 10, paddingHorizontal: 14, paddingBottom: 8 },
  detail: { flex: 1, gap: 8 }, text: { color: c.muted, fontSize: 13, lineHeight: 19 },
  preview: { width: 44, height: 44, borderRadius: 8 },
  track: { height: 4, borderRadius: 2, backgroundColor: c.line, overflow: 'hidden' },
  fill: { height: 4, backgroundColor: c.accent },
  backdrop: { flex: 1, justifyContent: 'flex-end', backgroundColor: '#00000088', padding: 16, paddingBottom: 36 },
  sheet: { backgroundColor: c.surface, borderRadius: 20, padding: 20, gap: 12 },
  title: { color: c.text, fontSize: 20, fontWeight: '700' },
  choice: { minHeight: 48, justifyContent: 'center', borderTopWidth: StyleSheet.hairlineWidth, borderColor: c.line },
  choiceText: { color: c.text, fontSize: 16, fontWeight: '600' },
  notice: { color: c.warn, fontSize: 14, paddingVertical: 8 },
});
