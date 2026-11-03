import { useCallback, useRef, useState } from "react";
import {
  Alert,
  KeyboardAvoidingView,
  Platform,
  Pressable,
  StyleSheet,
  Text,
  View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { WebView } from "react-native-webview";
import { router, useFocusEffect } from "expo-router";
import { usePartyline } from "../runtime";
import type { StreamHandle } from "../runtime/terminal-stream";
import { streamTerminalHTML } from "./stream-terminal/bundle";
import { terminalBridgeMessage } from "./stream-terminal/bridge";
import { c } from "./theme";
export function StreamTerminalScreen({
  computer,
  session,
}: {
  computer: string;
  session: string;
}) {
  const app = usePartyline(),
    open = app.openTerminalStream;
  const host = app.computers.find((k) => k.id === computer);
  const connected = host?.connection === "connected";
  const web = useRef<WebView<unknown>>(null),
    handle = useRef<StreamHandle | undefined>(undefined),
    actual = useRef(session === "new" ? undefined : session),
    viewport = useRef({ columns: 80, rows: 24 });
  const request = useRef(crypto.randomUUID());
  const [loaded, setLoaded] = useState(false),
    [status, setStatus] = useState("Connecting terminal…"),
    [attempt, setAttempt] = useState(0);
  const post = (m: unknown) =>
    web.current?.injectJavaScript(
      `window.terminalReceive&&window.terminalReceive(${JSON.stringify(m).replace(/</g, "\\u003c")});true;`,
    );
  useFocusEffect(
    useCallback(() => {
      if (!loaded || !connected || !open) return;
      let alive = true;
      handle.current = open(
        computer,
        actual.current,
        viewport.current.columns,
        viewport.current.rows,
        (event) => {
          if (!alive) return;
          if (event.type === "ready") {
            actual.current = event.session;
            setStatus("");
            post({ type: "ready" });
          } else if (event.type === "output") post(event);
          else {
            setStatus(event.reason);
            post({ type: "closed" });
          }
        },
        request.current,
      );
      return () => {
        alive = false;
        handle.current?.close();
        handle.current = undefined;
        post({ type: "closed" });
      };
    // Explicit reconnect retries must reopen even when connection state is unchanged.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [loaded, connected, open, computer, attempt]),
  );
  return (
    <SafeAreaView edges={["top", "bottom", "left", "right"]} style={s.body}>
      <View style={s.bar}>
        <Pressable
          accessibilityRole="button"
          accessibilityLabel="Back"
          onPress={() => router.back()}
        >
          <Text style={s.action}>‹ Back</Text>
        </Pressable>
        <View style={s.title}>
          <Text style={s.text}>Phone terminal</Text>
          <Text style={s.muted}>{host?.name}</Text>
        </View>
        <Pressable
          accessibilityRole="button"
          accessibilityLabel="End terminal"
          onPress={() =>
            Alert.alert(
              "End terminal?",
              "This ends the shell session. Detached background jobs may continue.",
              [
                { text: "Cancel", style: "cancel" },
                {
                  text: "End terminal",
                  style: "destructive",
                  onPress: () => {
                    if (actual.current && app.endPhoneTerminal)
                      void app
                        .endPhoneTerminal(computer, actual.current)
                        .then(() => router.back())
                        .catch((e) => setStatus(String(e)));
                  },
                },
              ],
            )
          }
        >
          <Text style={s.action}>End</Text>
        </Pressable>
      </View>
      <KeyboardAvoidingView
        style={s.body}
        behavior={Platform.OS === "ios" ? "padding" : "height"}
      >
        {status || !connected ? (
          <View style={s.bar}>
            <Text style={[s.muted, s.title]}>
              {connected
                ? status
                : "Disconnected; output retained. Reattaching when connected."}
            </Text>
            <Pressable
              accessibilityRole="button"
              onPress={() => {
                setStatus("Connecting terminal…");
                setAttempt((x) => x + 1);
              }}
            >
              <Text style={s.action}>Reconnect</Text>
            </Pressable>
          </View>
        ) : null}
        <WebView
          ref={web}
          source={{ html: streamTerminalHTML }}
          style={s.body}
          originWhitelist={["about:*"]}
          onShouldStartLoadWithRequest={(r) => r.url === "about:blank"}
          domStorageEnabled={false}
          allowFileAccess={false}
          allowFileAccessFromFileURLs={false}
          allowUniversalAccessFromFileURLs={false}
          mixedContentMode="never"
          keyboardDisplayRequiresUserAction={false}
          hideKeyboardAccessoryView
          onContentProcessDidTerminate={() => {
            handle.current?.close();
            setLoaded(false);
            web.current?.reload();
          }}
          onMessage={(event) => {
            try {
              const m = terminalBridgeMessage(event.nativeEvent.data);
              if (m.type === "loaded") setLoaded(true);
              else if (m.type === "notice" && typeof m.text === "string")
                setStatus(m.text);
              else if (m.type === "input" && typeof m.data === "string")
                handle.current?.input(m.data);
              else if (m.type === "ack" && typeof m.sequence === "number" && Number.isSafeInteger(m.sequence))
                handle.current?.ack(m.sequence);
              else if (
                m.type === "viewport" &&
                typeof m.columns === "number" && typeof m.rows === "number" &&
                Number.isInteger(m.columns) &&
                Number.isInteger(m.rows)
              ) {
                viewport.current = {
                  columns: Math.max(20, Math.min(300, m.columns)),
                  rows: Math.max(8, Math.min(200, m.rows)),
                };
                if (handle.current)
                  handle.current.resize(
                    viewport.current.columns,
                    viewport.current.rows,
                  );
              } else if (m.type === "paste" && typeof m.text === "string")
                Alert.alert(
                  "Paste multiple lines?",
                  "These lines may run commands immediately.",
                  [
                    { text: "Cancel", style: "cancel" },
                    {
                      text: "Paste",
                      onPress: () => post({ type: "paste", text: m.text }),
                    },
                  ],
                );
            } catch (error) {
              setStatus(String(error));
            }
          }}
        />
        <View style={s.keys}>
          {[
            ["Esc", "Escape"],
            ["Ctrl-C", "C-c"],
            ["Ctrl-Z", "C-z"],
            ["Tab", "Tab"],
            ["↑", "Up"],
            ["↓", "Down"],
            ["←", "Left"],
            ["→", "Right"],
          ].map(([label, key]) => (
            <Pressable
              key={key}
              accessibilityRole="button"
              accessibilityLabel={label}
              style={s.key}
              onPress={() => post({ type: "key", key })}
            >
              <Text style={s.text}>{label}</Text>
            </Pressable>
          ))}
          <Pressable
            accessibilityRole="button"
            accessibilityLabel="Show terminal keyboard"
            style={s.key}
            onPress={() => post({ type: "focus" })}
          >
            <Text style={s.text}>Type</Text>
          </Pressable>
        </View>
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
}
const s = StyleSheet.create({
  body: { flex: 1, backgroundColor: c.term },
  bar: {
    padding: 12,
    flexDirection: "row",
    alignItems: "center",
    gap: 12,
    backgroundColor: c.ground,
  },
  title: { flex: 1 },
  text: { color: c.text, fontSize: 13 },
  muted: { color: c.muted, fontSize: 12 },
  action: { color: c.accent, fontSize: 14 },
  keys: { flexDirection: "row", paddingVertical: 4 },
  key: {
    flex: 1,
    minHeight: 42,
    alignItems: "center",
    justifyContent: "center",
  },
});
