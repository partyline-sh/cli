import { createElement } from "react";
import { act, create, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { Computer, LoopItem } from "../src/runtime/types";
import { LoopDetailsScreen } from "../src/ui/LoopDetailsScreen";
const fixture = vi.hoisted(() => ({
  computers: [] as Computer[],
  loops: [] as LoopItem[],
  loading: false,
  deliveries: [],
  back: vi.fn(),
  replace: vi.fn(),
  canGoBack: false,
}));
vi.mock("expo-router", () => ({
  router: {
    canGoBack: () => fixture.canGoBack,
    back: fixture.back,
    replace: fixture.replace,
  },
}));
vi.mock("../src/runtime", () => ({ usePartyline: () => fixture }));
vi.mock("react-native", () => ({
  KeyboardAvoidingView: "KeyboardAvoidingView",
  Pressable: "Pressable",
  ScrollView: "ScrollView",
  Text: "Text",
  View: "View",
  StyleSheet: { create: (s: unknown) => s },
  Platform: { select: () => "monospace" },
}));
vi.mock("react-native-safe-area-context", () => ({
  SafeAreaView: "SafeAreaView",
}));
vi.mock("../src/ui/parts", () => ({ Empty: "Empty" }));
let rendered: ReactTestRenderer | undefined;
const render = async () =>
  act(async () => {
    const element = createElement(LoopDetailsScreen, {
      computer: "mac",
      id: "same",
    });
    if (rendered) rendered.update(element);
    else rendered = create(element);
  });
const content = () => JSON.stringify(rendered!.toJSON());
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  fixture.back.mockReset();
  fixture.replace.mockReset();
  fixture.canGoBack = false;
  fixture.computers = [
    {
      id: "mac",
      name: "Mac",
      relay: "https://relay.example",
      connection: "connected",
      pairedAt: 1,
      capabilities: [],
    },
  ];
  fixture.loops = [
    {
      id: "same",
      computerId: "other",
      title: "Wrong computer",
      section: "Done",
      state: "done",
    },
    {
      id: "same",
      computerId: "mac",
      title: "Correct task",
      section: "In review",
      state: "reviewing",
      detail: "Retry flow review",
      round: 2,
      openP1: 0,
      pr: 42,
      ci: "pending",
      chips: [{ name: "checks", mark: "done" }],
    },
  ];
});
afterEach(async () => {
  await act(async () => rendered?.unmount());
  rendered = undefined;
});
it("shows the selected computer’s live loop, including zero findings, and follows stage changes", async () => {
  await render();
  expect(content()).toContain("Correct task");
  expect(content()).not.toContain("Wrong computer");
  expect(content()).toContain("Retry flow review");
  expect(content()).toContain("#42");
  expect(content()).toContain("Pending");
  const fields = rendered!.root
    .findAllByType("Text" as never)
    .map((n) => n.props.children);
  expect(fields).toContain(0);
  fixture.loops = fixture.loops.map((l) =>
    l.computerId === "mac"
      ? { ...l, section: "Done", state: "done", ci: "pass" }
      : l,
  );
  await render();
  expect(content()).toContain("Passed");
  expect(content()).not.toContain("reviewing");
});
it("labels missing data as unreported and warns when showing a disconnected snapshot", async () => {
  fixture.loops = [
    {
      id: "same",
      computerId: "mac",
      title: "Old host",
      section: "Done",
      state: "done",
    },
  ];
  fixture.computers[0] = { ...fixture.computers[0], connection: "offline" };
  await render();
  expect(content()).toContain("Not reported");
  expect(content()).toContain("may be out of date");
  expect(content()).not.toContain("Passed");
});
it("handles removed loops and deep-link Back without selecting another computer’s loop", async () => {
  fixture.loops = fixture.loops.filter((l) => l.computerId !== "mac");
  await render();
  expect(content()).toContain("no longer on the board");
  await act(async () =>
    rendered!.root
      .findByProps({ accessibilityLabel: "Back to Loops" })
      .props.onPress(),
  );
  expect(fixture.replace).toHaveBeenCalledWith("/loops");
});
