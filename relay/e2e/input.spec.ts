import { test, expect } from "@playwright/test";
import { spawn } from "node:child_process";

test("encrypted input, keys, paste, chat, drafts and explicit delivery recovery", async ({ page }) => {
  test.setTimeout(120000);
  await page.setViewportSize({ width: 390, height: 844 });
  const child = spawn("go", ["run", "./relay/testdata/channel-machine", process.env.PWA_ORIGIN ?? "http://localhost:8787"], {
    cwd: "..", env: { ...process.env, PWA_UI: "1" }, stdio: ["pipe", "pipe", "pipe"],
  });
  let output = "", errors = "";
  child.stderr.on("data", b => errors += b);
  child.stdout.on("data", b => output += b);
  const commands = () => output.trim().split("\n").slice(1).filter(Boolean).map(line => JSON.parse(line));
  try {
    await expect.poll(() => output.includes("\n"), { timeout: 90000, message: errors }).toBe(true);
    const saved = JSON.parse(output.split("\n")[0]);
    await page.goto("/");
    await page.evaluate(saved => localStorage.setItem(`partyline.phone.${saved.machine}`, JSON.stringify(saved)), saved);
    await page.reload();
    await page.getByRole("button", { name: /Builder/ }).click();
    const box = page.getByRole("textbox", { name: "Message", exact: true });
    const send = page.getByRole("button", { name: "Send", exact: true });
    await expect(page.locator(".xterm-rows")).toContainText("fresh generation");
    await page.setViewportSize({ width: 390, height: 360 });
    await box.focus();
    const composer = page.getByRole("region", { name: "Session composer" });
    await expect(composer).toBeVisible();
    await expect.poll(async () => (await composer.boundingBox())!.y + (await composer.boundingBox())!.height).toBeLessThanOrEqual(360);
    await page.setViewportSize({ width: 390, height: 844 });
    await box.fill("draft survives");
    await box.press("Enter");
    await box.pressSequentially("second line");
    await page.reload();
    await expect(box).toHaveValue("draft survives\nsecond line");
    await expect(page.getByText("Connected", { exact: true })).toBeVisible();
    await send.click();
    await expect(page.getByText("Confirmed", { exact: true })).toBeVisible();
    expect(commands().at(-1)).toMatchObject({ type: "input", kind: "text", data: "draft survives\nsecond line" });
    await page.getByRole("button", { name: "ctrl-c", exact: true }).click();
    await expect.poll(() => commands().at(-1)?.data).toBe("C-c");
    await expect(page.getByText("Confirmed", { exact: true })).toBeVisible();
    const beforeKeys = commands().length;
    // Queue several presses within one reply batch, rather than waiting between keys.
    await page.evaluate(() => {
      for (const label of ["↑", "↑", "esc", "ctrl-c"]) {
        const button = [...document.querySelectorAll(".key-row button")].find(b => b.textContent === label) as HTMLButtonElement;
        button.click();
      }
    });
    await expect.poll(() => commands().length).toBe(beforeKeys + 4);
    expect(commands().slice(beforeKeys).map(c => c.data)).toEqual(["Up", "Up", "Escape", "C-c"]);
    expect(new Set(commands().slice(beforeKeys).map(c => c.id)).size).toBe(4);
    await expect(page.getByText("Confirmed", { exact: true })).toBeVisible();
    await box.fill("paste\nlines");
    await page.getByRole("button", { name: "Paste", exact: true }).click();
    await expect(page.getByText("Confirmed", { exact: true })).toBeVisible();
    expect(commands().at(-1)).toMatchObject({ kind: "paste", data: "paste\nlines" });
    const generation = (await page.locator(".xterm-rows").textContent())!.match(/fresh generation (\d+)/)![1];
    const count = commands().length;
    await page.evaluate(() => window.dispatchEvent(new Event("online")));
    await expect.poll(async () => (await page.locator(".xterm-rows").textContent())?.match(/fresh generation (\d+)/)?.[1]).not.toBe(generation);
    expect(commands()).toHaveLength(count);
    await box.fill("unanswered");
    await send.click();
    await expect(page.getByText("Not confirmed — delivery unknown", { exact: true })).toBeVisible({ timeout: 10000 });
    const unanswered = commands().at(-1);
    await page.getByRole("button", { name: "Retry", exact: true }).click();
    await expect(page.getByText("Not confirmed — delivery unknown", { exact: true })).toBeVisible({ timeout: 10000 });
    expect(commands().filter(c => c.id === unanswered.id)).toHaveLength(1);
    await page.reload();
    await expect(page.getByText("Not confirmed — delivery unknown", { exact: true })).toBeVisible();
    await expect(page.locator(".xterm-rows")).toContainText("fresh generation");
    expect(commands()).toHaveLength(count + 1);
    const [warning] = await Promise.all([
      page.waitForEvent("dialog").then(async dialog => {
        const message = dialog.message();
        await dialog.accept();
        return message;
      }),
      page.getByRole("button", { name: "Send a new command…", exact: true }).click(),
    ]);
    expect(warning).toContain("could run the same action twice");
    await page.getByRole("button", { name: "Chat", exact: true }).click();
    await box.fill("chat from phone");
    await send.click();
    await expect(page.getByText("Confirmed", { exact: true })).toBeVisible();
    expect(commands().at(-1)).toMatchObject({ type: "chat_send", text: "chat from phone" });
    await expect(box).toHaveValue("");
    await box.fill("refused draft");
    await send.click();
    await expect(page.getByText("Refused: test refusal", { exact: true })).toBeVisible();
    await expect(box).toHaveValue("refused draft");
    await page.reload();
    await expect(box).toHaveValue("refused draft");
    await box.fill("<".repeat(24000));
    await send.click();
    await expect(page.getByRole("status").filter({ hasText: "transport frame" })).toBeVisible();
    expect(commands()).toHaveLength(count + 3);
  } finally { child.stdin.end(); child.kill("SIGTERM"); }
});
