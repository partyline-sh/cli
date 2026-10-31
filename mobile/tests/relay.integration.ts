import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";
import { expect, it } from "vitest";
import {
  PhoneSession,
  type InnerMessage,
  type SavedPhone,
} from "../src/runtime/protocol/session";

// Start the local relay on 8897 with the same fixture bindings as relay/playwright.config.ts.
// This uses no personal pairing, live tmux socket, deployed Worker or user credentials.
it("native transport interoperates with the Go responder through a local relay", async () => {
  const origin = "http://localhost:8897";
  const child = spawn(
    "go",
    ["run", "./relay/testdata/channel-machine", origin],
    {
      cwd: fileURLToPath(new URL("../../", import.meta.url)),
      env: { ...process.env, PWA_UI: "1" },
      stdio: ["pipe", "pipe", "pipe"],
    },
  );
  let output = "",
    errors = "";
  child.stdout.on("data", (bytes) => (output += bytes));
  child.stderr.on("data", (bytes) => (errors += bytes));
  const sessions: PhoneSession[] = [];
  try {
    await expect
      .poll(() => output.includes("\n"), { timeout: 90000, message: errors })
      .toBe(true);
    const saved = JSON.parse(output.split("\n")[0]) as SavedPhone;
    const received: InnerMessage[] = [];
    const session = new PhoneSession(saved, (message) => {
      received.push(message);
      if (message.type === "hello") {
        session.subscribe("sessions");
        session.subscribe("%1");
      }
    });
    sessions.push(session);
    session.connect(origin);
    await expect
      .poll(
        () =>
          received.some(
            (m) =>
              m.type === "out" &&
              typeof m.data === "string" &&
              m.data.includes("fresh generation"),
          ),
        { timeout: 10000 },
      )
      .toBe(true);
    session.send([
      {
        type: "input",
        id: "native-wire-command",
        pane: "%1",
        kind: "text",
        data: "hello from native transport",
      },
    ]);
    await expect
      .poll(
        () =>
          received.find(
            (m) => m.type === "input_result" && m.id === "native-wire-command",
          )?.status,
        { timeout: 10000 },
      )
      .toBe("ok");
    expect(JSON.parse(output.trim().split("\n")[1])).toMatchObject({
      id: "native-wire-command",
      data: "hello from native transport",
    });
    session.close();
    await new Promise((resolve) => setTimeout(resolve, 600));
    const reconnected: InnerMessage[] = [];
    const next = new PhoneSession(saved, (message) => {
      reconnected.push(message);
      if (message.type === "hello") next.subscribe("%1");
    });
    sessions.push(next);
    next.connect(origin);
    await expect
      .poll(() => reconnected.some((m) => m.type === "out"), { timeout: 10000 })
      .toBe(true);
    expect(output.trim().split("\n")).toHaveLength(2);
  } finally {
    sessions.forEach((session) => session.close());
    child.stdin.end();
    child.kill("SIGTERM");
  }
});
