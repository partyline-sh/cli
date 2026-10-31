import { FullLoopDetails } from "./FullLoopDetails";
import { router } from "expo-router";
import {
  KeyboardAvoidingView,
  Platform,
  ScrollView,
  Pressable,
  StyleSheet,
  Text,
  View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { usePartyline } from "../runtime";
import { Empty } from "./parts";
import { c, clock, mono } from "./theme";

function Field({
  label,
  value,
}: {
  label: string;
  value: string | number | undefined;
}) {
  return (
    <View style={s.field}>
      <Text style={s.label}>{label}</Text>
      <Text selectable style={s.value}>
        {value === undefined || value === "" ? "Not reported" : value}
      </Text>
    </View>
  );
}

/** Live board summary plus full details and authenticated phone controls. */
export function LoopDetailsScreen({
  computer,
  id,
}: {
  computer: string;
  id: string;
}) {
  const app = usePartyline();
  const host = app.computers.find((item) => item.id === computer);
  const loop = app.loops.find(
    (item) => item.computerId === computer && item.id === id,
  );
  const offline = host?.connection !== "connected";
  return (
    <SafeAreaView edges={["top", "bottom"]} style={s.screen}>
      <View style={s.bar}>
        <Pressable
          accessibilityRole="button"
          accessibilityLabel="Back to Loops"
          hitSlop={12}
          onPress={() =>
            router.canGoBack() ? router.back() : router.replace("/loops")
          }
        >
          <Text style={s.back}>‹</Text>
        </Pressable>
        <Text style={s.heading}>Loop details</Text>
        <View style={s.spacer} />
      </View>
      {!loop ? (
        <Empty>
          {app.loading
            ? "Loading loop…"
            : !host
              ? "This computer is not paired with this phone."
              : offline
                ? "Connect the computer to load this loop."
                : "This loop is no longer on the board."}
        </Empty>
      ) : (
        <KeyboardAvoidingView style={{ flex: 1 }} behavior={Platform.OS === "ios" ? "padding" : "height"}>
          <ScrollView keyboardShouldPersistTaps="handled" contentContainerStyle={s.content}>
            <Text selectable style={s.title}>
              {loop.title || loop.id}
            </Text>
            <Text style={s.subtitle}>{host?.name ?? computer}</Text>
            {offline ? (
              <Text accessibilityLiveRegion="polite" style={s.warning}>
                Offline — these details may be out of date.
              </Text>
            ) : null}
            {loop.receivedAt ? (
              <Text style={s.subtitle}>Received {clock(loop.receivedAt)}</Text>
            ) : null}
            <View style={s.panel}>
              <Field label="Loop" value={loop.id} />
              <Field label="Repository" value={loop.repo} />
              <Field label="Stage" value={loop.section} />
              <Field label="State" value={loop.state} />
            </View>
            <View style={s.panel}>
              <Text style={s.section}>Summary</Text>
              <Text selectable style={s.value}>
                {loop.detail || "No summary reported."}
              </Text>
            </View>
            <View style={s.panel}>
              <Text style={s.section}>Progress</Text>
              <Field label="Step" value={loop.batch} />
              <Field label="Round" value={loop.round} />
              <Field label="Builder" value={loop.builder} />
            </View>
            <View style={s.panel}>
              <Text style={s.section}>Checks and review</Text>
              <Field label="Open P1 findings" value={loop.openP1} />
              {loop.chips?.length ? (
                loop.chips.map((chip, index) => (
                  <Field
                    key={`${chip.name}:${index}`}
                    label={chip.name}
                    value={chip.mark}
                  />
                ))
              ) : (
                <Text style={s.subtitle}>
                  No check or reviewer details reported.
                </Text>
              )}
              {loop.dispute ? (
                <>
                  <Text style={s.label}>Disputed finding</Text>
                  <Text selectable style={s.value}>
                    {loop.dispute}
                  </Text>
                </>
              ) : null}
            </View>
            <View style={s.panel}>
              <Text style={s.section}>Pull request and CI</Text>
              <Field
                label="Pull request"
                value={loop.pr ? `#${loop.pr}` : undefined}
              />
              <Field
                label="CI"
                value={
                  loop.ci === "pass"
                    ? "Passed"
                    : loop.ci === "fail"
                      ? "Failed"
                      : loop.ci === "pending"
                        ? "Pending"
                        : loop.ci
                }
              />
            </View>
            <FullLoopDetails key={`${computer}:${id}`} computer={computer} id={id} receivedAt={loop.receivedAt} />
          </ScrollView>
        </KeyboardAvoidingView>
      )}
    </SafeAreaView>
  );
}
const s = StyleSheet.create({
  screen: { flex: 1, backgroundColor: c.ground },
  bar: {
    flexDirection: "row",
    alignItems: "center",
    paddingHorizontal: 16,
    paddingBottom: 10,
    borderBottomWidth: StyleSheet.hairlineWidth,
    borderBottomColor: c.line,
  },
  back: { color: c.accent, fontSize: 32, lineHeight: 34, width: 32 },
  heading: {
    flex: 1,
    textAlign: "center",
    color: c.text,
    fontSize: 17,
    fontWeight: "600",
  },
  spacer: { width: 32 },
  content: { padding: 16, gap: 14, paddingBottom: 28 },
  title: { color: c.text, fontSize: 24, fontWeight: "700", lineHeight: 30 },
  subtitle: { color: c.muted, fontSize: 13, lineHeight: 19 },
  warning: { color: c.warn, fontSize: 13, lineHeight: 19 },
  panel: { padding: 14, gap: 12, backgroundColor: c.surface, borderRadius: 14 },
  section: { color: c.text, fontSize: 16, fontWeight: "700" },
  field: { gap: 4 },
  label: { color: c.muted, fontSize: 12, fontFamily: mono },
  value: { color: c.text, fontSize: 14, lineHeight: 21 },
});
