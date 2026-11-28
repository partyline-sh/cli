import { test, expect } from "@playwright/test";
import { spawn } from "node:child_process";

test("session stays terminal-only even when native conversation is available", async ({ page }) => {
  test.setTimeout(120000);
  await page.setViewportSize({ width: 390, height: 844 });
  const child = spawn("go", ["run", "./relay/testdata/channel-machine", process.env.PWA_ORIGIN ?? "http://localhost:8787"], {
    cwd: "..", env: { ...process.env, PWA_UI: "1", PWA_TRANSCRIPT: "1" }, stdio: ["pipe", "pipe", "pipe"],
  });
  let output = "", errors = "";
  child.stdout.on("data", b => output += b);
  child.stderr.on("data", b => errors += b);
  try {
    await expect.poll(() => output.includes("\n"), { timeout: 90000, message: errors }).toBe(true);
    const saved = JSON.parse(output.split("\n")[0]);
    await page.goto("/");
    await page.evaluate(saved => localStorage.setItem(`partyline.phone.${saved.machine}`, JSON.stringify(saved)), saved);
    await page.reload();
    await page.getByRole("button", { name: /Builder/ }).click();
    await expect(page.getByLabel("Session terminal")).toBeVisible();
    await expect(page.getByLabel("Session messages")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Messages", exact: true })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Terminal", exact: true })).toHaveCount(0);
    await expect(page.locator(".xterm-rows")).toContainText("fresh generation");
    await expect(page.getByRole("textbox", { name: "Message", exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Back to Sessions" }).click();
    await page.getByRole("button", { name: /Reviewer/ }).click();
    await expect(page.getByLabel("Session terminal")).toBeVisible();
    await expect(page.getByRole("button", { name: "Messages", exact: true })).toHaveCount(0);
    await expect(page.locator(".xterm-rows")).toContainText("contractor review snapshot");
    expect(await page.evaluate(() => Object.values(localStorage).join(" "))).not.toContain("Reply line one");
  } finally { child.stdin.end(); child.kill("SIGTERM"); }
});
