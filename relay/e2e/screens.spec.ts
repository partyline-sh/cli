import { test, expect, type Page } from "@playwright/test";
import { build } from "esbuild";

// Replace only transport: these fixtures exercise the shipped app, screens and CSS.
let app: string;
test.beforeAll(async () => {
  const result = await build({
    entryPoints: ["src/app.ts"],
    bundle: true,
    format: "esm",
    write: false,
    outdir: "fixture",
    plugins: [
      {
        name: "fixture-transport",
        setup(build) {
          build.onResolve({ filter: /^\.\/session$/ }, () => ({
            path: "session",
            namespace: "fixture",
          }));
          build.onLoad({ filter: /.*/, namespace: "fixture" }, () => ({
            contents: `
        export class PhoneSession {
          constructor(saved, receive, closed) { this.saved = saved; this.receive = receive; this.closed = closed; }
          connect() { queueMicrotask(() => this.receive({type: "hello"})); }
          close() { this.closed("Session closed"); }
          unsubscribe() {}
          subscribe(topic) {
            const second = this.saved.machine === "Office Mac";
            const data = topic === "sessions" ? [
              {pane: "%1", title: second ? "Office build" : "Builder", engine: "codex", state: "working", lastLine: "Running checks", width: "100", height: "100"},
              {pane: "%2", title: second ? "Office review" : "Review mobile", engine: "claude", state: "approval", lastLine: "Ready for you", width: "80", height: "24"},
              {pane: "%3", title: "Shell", engine: "shell", state: "idle"}
            ] : topic === "board" ? [{title: "Ship mobile", section: "Running", state: "building", repo: "partyline", detail: "Phone screens"}]
            : topic === "chat" ? {messages: [{name: "darcy", text: "Phone check", at: "09:41"}]}
            // The computer marker plus 99 lines fills the 100-row pane without scrolling the marker away.
            : this.saved.machine + "\\r\\n" + Array.from({length: 99}, (_, n) => "terminal line " + n).join("\\r\\n");
            queueMicrotask(() => this.receive({type: topic.startsWith("%") ? "out" : topic, pane: topic.startsWith("%") ? topic : undefined, data}));
          }
        }
      `,
          }));
        },
      },
    ],
  });
  app = result.outputFiles.find((f) => f.path.endsWith(".js"))!.text;
});
async function paired(page: Page) {
  await page.route("**/app.js", (route) =>
    route.fulfill({ contentType: "text/javascript", body: app }),
  );
  await page.addInitScript(() => {
    for (const machine of ["MacBook Pro", "Office Mac"])
      localStorage.setItem(
        `partyline.phone.${machine}`,
        JSON.stringify({
          machine,
          phone: "fixture",
          token: "fixture",
          private: "fixture",
          public: "fixture",
        }),
      );
  });
  await page.goto("/");
}
async function noOverflow(page: Page) {
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
}
for (const width of [390, 430]) {
  test(`phone screens at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 844 });
    await paired(page);
    const nav = page.getByRole("navigation", { name: "Tabs" });
    await expect(nav.getByRole("button")).toHaveCount(4);
    await expect(nav.locator(".badge")).toHaveText("2");
    for (const name of ["MacBook Pro", "Office Mac"]) {
      const group = page.getByRole("region", { name, exact: true });
      await expect(group.locator(".session-row").first()).toContainText(
        name === "MacBook Pro" ? "Review mobile" : "Office review",
      );
      await expect(group.locator(".session-row").first()).toContainText(
        "claude",
      );
      await expect(group.locator(".session-row").first()).toContainText(
        "Waiting",
      );
      await expect(group.locator(".session-row").nth(1)).toContainText(
        "Working",
      );
      await expect(group.locator(".session-row").last()).toContainText("Idle");
    }
    await noOverflow(page);
    await page
      .getByRole("region", { name: "MacBook Pro", exact: true })
      .getByRole("button", { name: /Builder/ })
      .click();
    await expect(page.locator("header")).toContainText("Builder");
    await expect(page.locator("header")).toContainText("codex · MacBook Pro");
    await expect(page.locator("header .pill")).toHaveText("Working");
    await expect(page.locator(".xterm-rows")).toContainText("MacBook Pro");
    await expect(page.locator(".xterm-rows")).toContainText("terminal line");
    const size = () =>
      page
        .locator(".xterm-rows")
        .evaluate((el) => parseFloat(getComputedStyle(el).fontSize));
    const large = await size();
    await page.getByRole("button", { name: "Small terminal font" }).click();
    await expect.poll(size).toBeLessThan(large);
    const small = await size();
    await page.getByRole("button", { name: "Medium terminal font" }).click();
    await expect.poll(size).toBeGreaterThan(small);
    await page.getByRole("button", { name: "Large terminal font" }).click();
    await expect.poll(size).toBeGreaterThan(small);
    const scroll = page.locator(".terminal-scroll");
    expect(
      await scroll.evaluate((el) => el.scrollHeight > el.clientHeight),
    ).toBe(true);
    expect(
      await scroll.evaluate((el) => el.scrollWidth <= el.clientWidth),
    ).toBe(true);
    await noOverflow(page);
    await page.getByRole("button", { name: "Back to Sessions" }).click();
    await expect(page.locator(".session-row")).toHaveCount(6);
    await page.getByRole("button", { name: /Office build/ }).click();
    await expect(page.locator("header")).toContainText("Office Mac");
    await expect(page.locator(".xterm-rows")).toContainText("Office Mac");
    await page.getByRole("button", { name: "Back to Sessions" }).click();
    await nav.getByRole("button", { name: "Loops", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "Ship mobile" }),
    ).toHaveCount(2);
    await expect(page.getByText("Phone screens", { exact: true })).toHaveCount(
      2,
    );
    await noOverflow(page);
    await nav.getByRole("button", { name: "Chat", exact: true }).click();
    await expect(page.getByText("Phone check", { exact: true })).toHaveCount(2);
    await expect(
      page.getByRole("heading", { name: "darcy", exact: true }),
    ).toHaveCount(2);
    await noOverflow(page);
    await nav.getByRole("button", { name: "Computers", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "MacBook Pro", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "Office Mac", exact: true }),
    ).toBeVisible();
    await expect(
      page.locator("#app input, #app textarea:not(.xterm-helper-textarea)"),
    ).toHaveCount(0);
    await noOverflow(page);
  });
}
const safari =
  "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Version/18.0 Mobile/15E148 Safari/604.1";
test.describe("Safari installation", () => {
  test.use({ userAgent: safari, viewport: { width: 390, height: 844 } });
  test("unpaired Safari gets the sheet and can continue to pairing", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(page.getByRole("dialog")).toBeVisible();
    await expect(page.getByRole("dialog")).toContainText("Share");
    await expect(page.getByRole("dialog")).toContainText(
      "depends on the device",
    );
    await noOverflow(page);
    await page.getByRole("button", { name: "Not now" }).click();
    await page.getByRole("button", { name: "Pair a computer" }).click();
    await expect(
      page.getByRole("heading", { name: "Pair with your computer" }),
    ).toBeVisible();
  });
  test("standalone has no install sheet", async ({ page }) => {
    await page.addInitScript(() =>
      Object.defineProperty(navigator, "standalone", { value: true }),
    );
    await page.goto("/");
    await expect(page.getByRole("dialog")).not.toBeVisible();
    await expect(
      page.getByRole("heading", { name: "Pair with your computer" }),
    ).toBeVisible();
  });
  test("pairing link bypasses the sheet", async ({ page }) => {
    await page.goto("/#p=invalid");
    await expect(page.getByRole("dialog")).not.toBeVisible();
    await expect(page.locator("body")).toHaveAttribute("data-view", "pair");
  });
  test("paired Safari has no automatic install sheet", async ({ page }) => {
    await paired(page);
    await expect(page.getByRole("dialog")).not.toBeVisible();
    await page.getByRole("button", { name: "Computers", exact: true }).click();
    await page
      .getByRole("button", { name: "Add to Home Screen", exact: true })
      .click();
    await expect(page.getByRole("dialog")).toBeVisible();
  });
});
test("Android install button uses the deferred browser prompt", async ({
  page,
}) => {
  await page.goto("/");
  await expect(
    page.getByRole("button", { name: "Install", exact: true }),
  ).not.toBeVisible();
  await page.evaluate(() => {
    const event = new Event("beforeinstallprompt", { cancelable: true });
    Object.assign(event, {
      prompt: async () => {
        document.body.dataset.prompted = "yes";
      },
      userChoice: Promise.resolve({ outcome: "accepted" }),
    });
    window.dispatchEvent(event);
  });
  await page.getByRole("button", { name: "Install", exact: true }).click();
  await expect(page.locator("body")).toHaveAttribute("data-prompted", "yes");
  await expect(
    page.getByRole("button", { name: "Install", exact: true }),
  ).not.toBeVisible();
});
