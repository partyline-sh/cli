// Uses the production phone session under Node's WebSocket implementation when
// the host cannot launch a browser. This does not replace the Playwright check.
import { spawn } from "node:child_process";
import assert from "node:assert/strict";
import { PhoneSession } from "../dist/session.js";

const origin = process.argv[2] ?? "http://localhost:8899";
const children = [];
let session;
let deadline;
let incoming = () => {};
function child(command, args, options = {}) {
  const p = spawn(command, args, {
    stdio: ["pipe", "pipe", "pipe"],
    env: process.env,
    ...options,
  });
  children.push(p);
  return p;
}
function line(p, predicate) {
  return new Promise((resolve, reject) => {
    let text = "",
      errors = "";
    p.stderr.on("data", (b) => (errors += b));
    p.on("error", reject);
    p.on("exit", () => reject(new Error(errors || "process exited")));
    p.stdout.on("data", (b) => {
      text += b;
      const lines = text.split("\n");
      text = lines.pop();
      for (const l of lines) if (predicate(l)) resolve(l);
    });
  });
}
try {
  if (!process.argv[2]) {
    const server = child(
      process.execPath,
      [
        "node_modules/wrangler/bin/wrangler.js",
        "dev",
        "--port",
        "8899",
        "--inspector-port",
        "0",
        "--compatibility-date",
        "2026-06-02",
        "--persist-to",
        ".wrangler/channel-smoke",
        "--var",
        "INITIAL_MACHINE:11111111111111111111111111111111",
        "--var",
        "INITIAL_HASH:4f2e8d65483c641648cdb374ae9d8abd368d269e4ddffe092a8237b8162cddd6",
      ],
      {
        env: {
          ...process.env,
          CHOKIDAR_USEPOLLING: "1",
          XDG_CONFIG_HOME: ".wrangler/config",
          WRANGLER_LOG_PATH: ".wrangler/logs/channel-smoke.log",
        },
      },
    );
    await Promise.race([
      line(server, (l) => l.includes("Ready on")),
      new Promise((_, reject) => {
        deadline = setTimeout(
          () => reject(new Error("Relay startup timed out")),
          30000,
        );
      }),
    ]);
    clearTimeout(deadline);
  }
  const machine = child(
    "go",
    ["run", "./relay/testdata/channel-machine", origin],
    { cwd: ".." },
  );
  const saved = JSON.parse(
    await Promise.race([
      line(machine, (l) => l.startsWith("{")),
      new Promise((_, reject) => {
        deadline = setTimeout(
          () => reject(new Error("Go startup timed out")),
          90000,
        );
      }),
    ]),
  );
  clearTimeout(deadline);
  globalThis.document = new EventTarget();
  document.hidden = false;
  const text = await new Promise((resolve, reject) => {
    deadline = setTimeout(() => reject(new Error("No screen received")), 15000);
    let screen, chat;
    session = new PhoneSession(
      saved,
      (m) => {
        incoming(m);
        if (m.type === "hello") {
          session.subscribe("%1");
          session.subscribe("chat");
        }
        if (m.type === "out") screen = m.data;
        if (m.type === "chat") chat = m.data;
        if (screen && chat) {
          assert.equal(chat, "long chat snapshot;".repeat(6000));
          resolve(screen);
        }
      },
      (reason) => reject(new Error(reason)),
    );
    session.connect(origin);
  });
  assert.equal(text, "\x1b[32mexpected encrypted screen\x1b[0m\n");
  let executionText = "";
  machine.stdout.on("data", b => executionText += b);
  const executions = () => executionText.trim().split("\n").filter(Boolean).map(line => JSON.parse(line));
  async function request(message, confirmed = true) {
    const result = await new Promise((resolve, reject) => {
      const timer = setTimeout(() => confirmed ? reject(new Error("No input_result")) : resolve(undefined), confirmed ? 10000 : 1200);
      incoming = m => {
        if (m.type === "input_result" && m.id === message.id) { clearTimeout(timer); resolve(m); }
      };
      session.send([message]);
    });
    if (confirmed) { assert.equal(result.id, message.id); assert.equal(result.status, "ok"); }
  }
  const commands = [
    { type: "input", id: "text-once", pane: "%1", kind: "text", data: "first\nsecond" },
    { type: "input", id: "key-once", pane: "%1", kind: "key", data: "C-c" },
    { type: "input", id: "paste-once", pane: "%1", kind: "paste", data: "-literal\npaste" },
    { type: "chat_send", id: "chat-once", text: "from the phone" },
  ];
  assert.throws(() => session.send([{ type: "input", id: "too-big", pane: "%1", kind: "text", data: "<".repeat(24000) }]), /transport frame/);
  for (const m of commands) await request(m);
  assert.equal(executions().length, 4);
  session.close();
  await new Promise(resolve => setTimeout(resolve, 600));
  await new Promise((resolve, reject) => {
    deadline = setTimeout(() => reject(new Error("Reconnect timed out")), 15000);
    session = new PhoneSession(saved, m => {
      incoming(m);
      if (m.type === "hello") { clearTimeout(deadline); resolve(); }
    }, reason => reject(new Error(reason)));
    session.connect(origin);
  });
  await new Promise(resolve => setTimeout(resolve, 600));
  assert.equal(executions().length, 4, "reconnect resent a command");
  for (const m of commands) await request(m);
  assert.equal(executions().length, 4, "duplicate after reconnect executed again");
  const unknown = { type: "input", id: "unknown-once", pane: "%1", kind: "text", data: "unanswered" };
  await request(unknown, false);
  await request(unknown, false);
  assert.equal(executions().filter(m => m.id === unknown.id).length, 1);
  console.log(
    "PASS: TypeScript PhoneSession → local relay → Go KK responder → zstd screen + fragmented chat; input/key/paste/chat acknowledgements and same-ID retry across reconnect",
  );
} finally {
  clearTimeout(deadline);
  session?.close();
  for (const p of children) {
    p.stdin.end();
    p.kill("SIGTERM");
  }
}
