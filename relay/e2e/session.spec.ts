import { test, expect } from "@playwright/test";
import { spawn } from "node:child_process";
import { once } from "node:events";

test("browser KK session decodes a zstd screen from the Go machine", async ({
  page,
}) => {
  test.setTimeout(120000);
  const child = spawn(
    "go",
    ["run", "./relay/testdata/channel-machine", "http://localhost:8787"],
    { cwd: "..", env: process.env, stdio: ["pipe", "pipe", "pipe"] },
  );
  let stderr = "";
  child.stderr.on("data", (b) => (stderr += b));
  try {
    const saved = await Promise.race([
      new Promise<object>((resolve, reject) => {
        let text = "";
        child.stdout.on("data", (b) => {
          text += b;
          if (text.includes("\n")) {
            try {
              resolve(JSON.parse(text.split("\n")[0]));
            } catch (e) {
              reject(e);
            }
          }
        });
      }),
      once(child, "exit").then(() => {
        throw new Error("Go double exited: " + stderr);
      }),
      new Promise<never>((_, reject) =>
        setTimeout(
          () => reject(new Error("Go double timed out: " + stderr)),
          90000,
        ),
      ),
    ]);
    await page.goto("/");
    const text = await page.evaluate(async (saved) => {
      const path = "/session.js";
      const { PhoneSession } = await import(path);
      return new Promise<string>((resolve, reject) => {
        const timer = setTimeout(() => reject(new Error("No screen")), 15000);
        let screen: string | undefined, chat: string | undefined;
        const session = new PhoneSession(
          saved,
          (message: { type: string; data: unknown }) => {
            if (message.type === "hello") {
              session.subscribe("%1");
              session.subscribe("chat");
            }
            if (message.type === "out") screen = message.data as string;
            if (message.type === "chat") chat = message.data as string;
            if (screen && chat) {
              if (chat !== "long chat snapshot;".repeat(6000)) {
                reject(new Error("Fragmented chat mismatch"));
                session.close();
                return;
              }
              clearTimeout(timer);
              resolve(screen);
              session.close();
            }
          },
          (reason: string) => {
            if (reason !== "Session closed") reject(new Error(reason));
          },
        );
        session.connect();
      });
    }, saved);
    expect(text).toBe("\x1b[32mexpected encrypted screen\x1b[0m\n");
  } finally {
    child.stdin.end();
    child.kill("SIGTERM");
  }
});
