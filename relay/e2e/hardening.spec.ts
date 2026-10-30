import { test, expect, type Page } from "@playwright/test";
import { spawn } from "node:child_process";

async function machine(page: Page, bye = false) {
  const child = spawn("go", ["run", "./relay/testdata/channel-machine", "http://localhost:8787"], {
    cwd: "..",
    env: { ...process.env, PWA_UI: "1", PWA_BYE: bye ? "1" : "" },
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
    await page.getByRole("button", { name: saved.machine }).click();
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
  const { child } = await machine(page);
  try {
    for (const label of ["script-link", "https-link"]) {
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
        throw new Error("Link text missing");
      }, label);
      await page.mouse.move(point.x, point.y);
      await page.waitForTimeout(150);
      await page.mouse.click(point.x, point.y);
    }
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

test("bye ends the session with the machine reason", async ({ page }) => {
  test.setTimeout(120000);
  const { child } = await machine(page, true);
  try {
    await expect(page.getByText("Session ended: Unreadable phone message", { exact: true })).toBeVisible();
    // Later output must not restore the live status.
    await page.waitForTimeout(1000);
    await expect(page.getByText("Session ended: Unreadable phone message", { exact: true })).toBeVisible();
  } finally { child.stdin.end(); child.kill("SIGTERM"); }
});
