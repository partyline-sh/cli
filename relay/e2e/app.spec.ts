import { test, expect } from "@playwright/test";
import { spawn } from "node:child_process";

test("read-only app navigates snapshots and redraws after foreground handshake", async ({
  page,
}) => {
  test.setTimeout(120000);
  const child = spawn(
    "go",
    ["run", "./relay/testdata/channel-machine", "http://localhost:8787"],
    {
      cwd: "..",
      env: { ...process.env, PWA_UI: "1" },
      stdio: ["pipe", "pipe", "pipe"],
    },
  );
  let errors = "";
  child.stderr.on("data", (b) => (errors += b));
  try {
    const saved = await new Promise<{ machine: string }>((resolve, reject) => {
      let output = "";
      const timer = setTimeout(
        () => reject(new Error("Machine timeout " + errors)),
        90000,
      );
      child.on("exit", () => {
        clearTimeout(timer);
        reject(new Error(errors));
      });
      child.stdout.on("data", (b) => {
        output += b;
        if (output.includes("\n")) {
          clearTimeout(timer);
          resolve(JSON.parse(output.split("\n")[0]));
        }
      });
    });
    await page.goto("/");
    await page.evaluate(
      (saved) =>
        localStorage.setItem(
          `partyline.phone.${saved.machine}`,
          JSON.stringify(saved),
        ),
      saved,
    );
    await page.reload();
    await page.getByRole("button", { name: saved.machine }).click();
    await page.getByRole("button", { name: /Builder/ }).click();
    await expect(page.locator(".xterm-rows")).toContainText(
      "fresh generation 1",
    );
    const first = await page.locator(".xterm-rows").textContent();
    await expect
      .poll(() => page.locator(".xterm-rows").textContent())
      .not.toBe(first);
    const arrivals = await page.locator(".xterm-rows").evaluate(
      (rows) =>
        new Promise<number[]>((resolve) => {
          const times: number[] = [];
          let previous = rows.textContent;
          const observer = new MutationObserver(() => {
            const text = rows.textContent;
            if (!text?.includes("fresh generation") || text === previous)
              return;
            previous = text;
            times.push(performance.now());
            if (times.length === 3) {
              observer.disconnect();
              resolve(times);
            }
          });
          observer.observe(rows, {
            subtree: true,
            childList: true,
            characterData: true,
          });
        }),
    );
    expect(arrivals[1] - arrivals[0]).toBeGreaterThanOrEqual(350);
    expect(arrivals[2] - arrivals[1]).toBeGreaterThanOrEqual(350);
    await page.getByRole("button", { name: "Board", exact: true }).click();
    await expect(page.getByText("Ship mobile")).toBeVisible();
    await page
      .getByRole("button", { name: "Contractors", exact: true })
      .click();
    await expect(page.getByText("reviewer · Review mobile")).toBeVisible();
    await page.getByRole("button", { name: "View pane", exact: true }).click();
    await expect(page.locator(".xterm-rows")).toContainText(
      "contractor review snapshot",
    );
    await page.getByRole("button", { name: "Chat", exact: true }).click();
    await expect(page.getByText("Phone check")).toBeVisible();
    await page.getByRole("button", { name: "Sessions", exact: true }).click();
    // Sessions are a retained inventory even when the server suppresses unchanged snapshots.
    await page.getByRole("button", { name: /Builder/ }).click();
    await page.evaluate(() => {
      Object.defineProperty(document, "hidden", {
        configurable: true,
        value: true,
      });
      document.dispatchEvent(new Event("visibilitychange"));
    });
    await expect(page.locator(".xterm-rows")).toHaveCount(0);
    await page.waitForTimeout(600);
    await page.evaluate(() => {
      Object.defineProperty(document, "hidden", {
        configurable: true,
        value: false,
      });
      document.dispatchEvent(new Event("visibilitychange"));
    });
    await expect(page.locator(".xterm-rows")).toContainText(
      "fresh generation 2",
    );
    await expect(
      page.locator("#app input, #app textarea:not(.xterm-helper-textarea)"),
    ).toHaveCount(0);
  } finally {
    child.stdin.end();
    child.kill("SIGTERM");
  }
});
