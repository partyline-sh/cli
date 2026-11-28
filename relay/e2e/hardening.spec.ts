import { test, expect, type Page } from "@playwright/test";
import { spawn } from "node:child_process";

async function machine(page: Page, bye = false, staticTerminal = false) {
  const child = spawn("go", ["run", "./relay/testdata/channel-machine", process.env.PWA_ORIGIN ?? "http://localhost:8787"], {
    cwd: "..",
    env: { ...process.env, PWA_UI: "1", PWA_BYE: bye ? "1" : "", PWA_STATIC_TERMINAL: staticTerminal ? "1" : "" },
    stdio: ["pipe", "pipe", "pipe"],
  });
  let errors = "";
  child.stderr.on("data", b => errors += b);
  try {
    const saved = await new Promise<{ machine: string }>((resolve, reject) => {
      let output = "";
      const timer = setTimeout(() => reject(new Error("Machine timeout: " + errors)), 90000);
      child.on("exit", () => { clearTimeout(timer); reject(new Error(errors)); });
      child.stdout.on("data", b => {
        output += b;
        if (output.includes("\n")) {
          clearTimeout(timer);
          resolve(JSON.parse(output.split("\n")[0]));
        }
      });
    });
    const response = await page.goto("/");
    await page.evaluate(saved => localStorage.setItem(
      `partyline.phone.${saved.machine}`, JSON.stringify(saved)), saved);
    await page.reload();
    await page.getByRole("button", { name: /Builder/ }).click();
    await expect(page.locator(".xterm-rows")).toContainText("fresh generation");
    return { child, response };
  } catch (e) {
    child.stdin.end();
    child.kill("SIGTERM");
    throw e;
  }
}

test("pane links open only http", async ({ page }) => {
  test.setTimeout(120000);
  await page.addInitScript(() => {
    (window as any).opened = [];
    window.open = (...args: any[]) => { (window as any).opened.push(args); return null; };
  });
  const { child } = await machine(page, false, true);
  try {
    // Keep this fixture's terminal unchanged across ticks. Real hover/click activation
    // must not race the streaming snapshot reset exercised by the other tests.
    const clickLabel = async (label: string, requireLink = false) => {
      const point = await page.locator(".xterm-rows").evaluate((rows, label) => {
        const walker = document.createTreeWalker(rows, NodeFilter.SHOW_TEXT);
        while (walker.nextNode()) {
          const node = walker.currentNode;
          const at = node.textContent?.indexOf(label) ?? -1;
          if (at < 0) continue;
          const range = document.createRange();
          range.setStart(node, at);
          range.setEnd(node, at + label.length);
          const r = range.getBoundingClientRect();
          return { x: r.x + r.width / 2, y: r.y + r.height / 2 };
        }
        return null;
      }, label);
      expect(point, `missing terminal label ${label}`).not.toBeNull();
      if (!point) throw new Error(`Missing ${label}`);
      await page.mouse.move(0, 0);
      await page.mouse.move(point.x, point.y);
      if (requireLink) await expect(page.locator(".xterm-screen")).toHaveClass(/xterm-cursor-pointer/);
      await page.mouse.click(point.x, point.y);
    };
    await clickLabel("https-link", true);
    await expect.poll(() => page.evaluate(() => (window as any).opened.length)).toBe(1);
    await clickLabel("script-link");
    expect(await page.evaluate(() => (window as any).opened)).toEqual([
      ["https://example.com/", "_blank", "noopener"],
    ]);
    expect(await page.evaluate(() => (window as any).paneScriptRan)).toBeUndefined();
  } finally { child.stdin.end(); child.kill("SIGTERM"); }
});

test("csp without violations while pane renders", async ({ page }) => {
  test.setTimeout(120000);
  await page.addInitScript(() => {
    (window as any).violations = [];
    document.addEventListener("securitypolicyviolation", e =>
      (window as any).violations.push({ directive: e.violatedDirective, blocked: e.blockedURI }));
  });
  const { child, response } = await machine(page);
  try {
    expect(response?.headers()["content-security-policy"]).toBe(
      "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'");
    await expect(page.locator(".xterm-rows")).toContainText("https-link");
    await page.waitForTimeout(600);
    expect(await page.evaluate(() => (window as any).violations)).toEqual([]);
  } finally { child.stdin.end(); child.kill("SIGTERM"); }
});

test("bye marks the retained output stale while reconnecting", async ({ page }) => {
  test.setTimeout(120000);
  const { child } = await machine(page, true);
  try {
    await expect(page.getByText(/^Reconnecting to /)).toBeVisible();
    await expect(page.locator(".stale")).toContainText("as of");
  } finally { child.stdin.end(); child.kill("SIGTERM"); }
});
