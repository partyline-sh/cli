import { Buffer } from "node:buffer";
import { chromium } from "../../relay/node_modules/playwright/index.mjs";
import { readFileSync } from "node:fs";
const source = readFileSync(
  new URL("../src/ui/stream-terminal/bundle.ts", import.meta.url),
  "utf8",
);
const html = JSON.parse(
  source
    .split("export const streamTerminalHTML = ")[1]
    .trim()
    .replace(/;$/, ""),
);
const browser = await chromium.launch({ headless: true });
try {
  const page = await browser.newPage({ viewport: { width: 390, height: 600 } });
  const errors = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  await page.evaluate(() => {
    window.events = [];
    window.ReactNativeWebView = {
      postMessage: (s) => window.events.push(JSON.parse(s)),
    };
  });
  await page.setContent(html);
  await page.waitForFunction(() =>
    window.events.some((e) => e.type === "loaded"),
  );
  const push = (m) => page.evaluate((m) => window.terminalReceive(m), m);
  await push({ type: "ready" });
  await push({
    type: "output",
    sequence: 1,
    data: Buffer.from([0x24, 0x20, 0xc3]).toString("base64"),
  });
  await push({
    type: "output",
    sequence: 2,
    data: Buffer.from([0xa9]).toString("base64"),
  });
  await page.waitForFunction(
    () => window.events.filter((e) => e.type === "ack").length === 2,
  );
  await page.waitForFunction(() =>
    document.querySelector(".xterm-rows").textContent.includes("é"),
  );
  const text = await page.locator(".xterm-rows").innerText();
  if (!text.includes("é") || text.includes("�"))
    throw Error("split UTF8 output corrupted");
  const acks = await page.evaluate(() =>
    window.events.filter((e) => e.type === "ack").map((e) => e.sequence),
  );
  if (JSON.stringify(acks) !== "[1,2]") throw Error("parser ACK order");
  await push({ type: "focus" });
  await page.keyboard.type("hello");
  await page.waitForTimeout(30);
  const inputs = await page.evaluate(() =>
    window.events.filter((e) => e.type === "input").map((e) => e.data),
  );
  if (
    Buffer.concat(inputs.map((s) => Buffer.from(s, "base64"))).toString() !==
    "hello"
  )
    throw Error("input bytes changed");
  await push({
    type: "output",
    sequence: 3,
    data: Buffer.from("\x1b[?1h").toString("base64"),
  });
  await page.waitForFunction(() =>
    window.events.some((e) => e.type === "ack" && e.sequence === 3),
  );
  await push({ type: "key", key: "Left" });
  await page.waitForTimeout(30);
  const arrow = await page.evaluate(
    () => window.events.filter((e) => e.type === "input").at(-1).data,
  );
  if (Buffer.from(arrow, "base64").toString() !== "\x1bOD")
    throw Error("application cursor mode lost");
  await push({ type: "closed" });
  await page.keyboard.type("discard");
  await page.waitForTimeout(30);
  const last = await page.evaluate(
    () => window.events.filter((e) => e.type === "input").at(-1).data,
  );
  if (last !== arrow) throw Error("closed input replayed");
  if (!(await page.locator(".xterm-rows").innerText()).includes("é"))
    throw Error("closed stream blanked terminal");
  await push({type:"ready"});
  await push({type:"output",sequence:99,data:Buffer.from(Array.from({length:300},(_,i)=>`scroll-line-${i}\r\n`).join("")).toString("base64")});
  await page.waitForFunction(()=>window.events.some(e=>e.type==="ack"&&e.sequence===99));
  await page.waitForTimeout(100);
  const bottom=await page.locator(".xterm-viewport").evaluate(e=>e.scrollTop);
  await push({type:"history"});await page.waitForTimeout(100);
  const older=await page.locator(".xterm-viewport").evaluate(e=>e.scrollTop);
  if(!(older<bottom))throw Error("History control did not reveal earlier output");
  await push({type:"latest"});await page.waitForTimeout(100);
  const latest=await page.locator(".xterm-viewport").evaluate(e=>e.scrollTop);
  if(latest<bottom)throw Error("Latest control did not restore the bottom");
  await push({type:"focus"});await push({type:"blur"});
  if(await page.evaluate(()=>document.activeElement?.tagName)==="TEXTAREA")throw Error("Hide keyboard did not blur terminal input");
  if (errors.length) throw Error(errors.join("\n"));
  console.log("stream terminal browser probe passed");
} finally {
  await browser.close();
}
