import { test, expect } from "@playwright/test";
import { spawn } from "node:child_process";

test("encrypted conversation updates, terminal toggle and shell fallback", async ({ page }) => {
  test.setTimeout(120000);
  await page.setViewportSize({ width: 390, height: 844 });
  const child = spawn("go", ["run", "./relay/testdata/channel-machine", "http://localhost:8787"], {
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
    const messages = page.getByLabel("Session messages");
    await expect(messages).toBeVisible();
    await expect(messages).toContainText("Please run checks");
    await expect(messages).toContainText("Reply line one\nline two");
    await expect(messages).toContainText("Bash · go test ./... · passed");
    await expect(messages).toContainText("Read · missing.go · failed");
    await expect(messages).toContainText("Edit · main.go · running");
    await messages.evaluate(el => el.scrollTop = 20);
    const before = await messages.textContent();
    await expect.poll(() => messages.textContent()).not.toBe(before);
    expect(await messages.evaluate(el => el.scrollTop)).toBe(20);
    await expect(page.getByRole("textbox", { name: "Message", exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Terminal", exact: true }).click();
    await expect(messages).toBeHidden();
    await expect(page.locator(".xterm-rows")).toContainText("fresh generation");
    await page.getByRole("button", { name: "Messages", exact: true }).click();
    await expect(messages).toBeVisible();
    await page.getByRole("button", { name: "Back to Sessions" }).click();
    await page.getByRole("button", { name: /Reviewer/ }).click();
    await expect(page.getByLabel("Session terminal")).toBeVisible();
    await expect(page.getByRole("button", { name: "Messages", exact: true })).toBeDisabled();
    await expect(page.locator(".xterm-rows")).toContainText("contractor review snapshot");
    expect(await page.evaluate(() => Object.values(localStorage).join(" "))).not.toContain("Reply line one");
  } finally { child.stdin.end(); child.kill("SIGTERM"); }
});
