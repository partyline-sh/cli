import { test, expect } from "@playwright/test";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { readFile, readdir } from "node:fs/promises";

test.use({ viewport: { width: 390, height: 844 } });

test("restores navigation, reconnects once for grouped triggers, refreshes and stops on revocation", async ({ page, request }) => {
  test.setTimeout(120000);
  const child = spawn("go", ["run", "./relay/testdata/channel-machine", "http://localhost:8787"], {
    cwd: "..", env: { ...process.env, PWA_UI: "1" }, stdio: ["pipe", "pipe", "pipe"],
  });
  let errors = "";
  child.stderr.on("data", b => errors += b);
  try {
    const saved = await new Promise<{ machine: string; phone: string; token: string }>((resolve, reject) => {
      let output = "";
      const timer = setTimeout(() => reject(new Error(errors || "Machine timeout")), 90000);
      child.on("exit", () => { clearTimeout(timer); reject(new Error(errors)); });
      child.stdout.on("data", b => {
        output += b;
        if (output.includes("\n")) { clearTimeout(timer); resolve(JSON.parse(output.split("\n")[0])); }
      });
    });
    await page.addInitScript(() => {
      const Original = window.WebSocket;
      Object.assign(window, { socketCount: 0 });
      window.WebSocket = class extends Original {
        constructor(url: string | URL, protocols?: string | string[]) {
          super(url, protocols);
          if (new URL(String(url)).pathname === "/connect") {
            const state = window as unknown as { socketCount: number; phoneSocket: WebSocket };
            state.socketCount++;
            state.phoneSocket = this;
          }
        }
      };
    });
    await page.goto("/");
    await page.evaluate(saved => localStorage.setItem(`partyline.phone.${saved.machine}`, JSON.stringify(saved)), saved);
    await page.reload();
    await page.getByRole("button", { name: /Builder/ }).click();
    await expect(page.locator(".xterm-rows")).toContainText("fresh generation");
    await page.reload();
    await expect(page.locator(".page-header")).toContainText("Builder");
    await expect(page.locator(".xterm-rows")).toContainText("fresh generation");
    expect(await page.evaluate(() => JSON.parse(localStorage.getItem("partyline.navigation")!))).toEqual({
      computer: saved.machine, tab: "Sessions", pane: "%1",
    });
    await page.getByRole("button", { name: "Loops", exact: true }).click();
    await page.reload();
    await expect(page.getByRole("button", { name: "Loops", exact: true })).toHaveAttribute("aria-current", "page");
    await expect(page.getByText("Ship mobile")).toBeVisible();
    await page.getByRole("button", { name: "Sessions", exact: true }).click();
    await page.getByRole("button", { name: /Builder/ }).click();
    await expect(page.locator(".xterm-rows")).toContainText("fresh generation");

    const count = await page.evaluate(() => {
      const state = window as unknown as { phoneSocket: WebSocket; socketCount: number };
      state.phoneSocket.close();
      return state.socketCount;
    });
    await expect(page.getByText(`Reconnecting to ${saved.machine}…`)).toBeVisible();
    await expect(page.locator(".stale")).toContainText("as of");
    const oldOutput = await page.locator(".xterm-rows").textContent();
    await page.evaluate(() => {
      window.dispatchEvent(new Event("online"));
      document.dispatchEvent(new Event("visibilitychange"));
      window.dispatchEvent(new Event("pageshow"));
    });
    await expect(page.locator(".connection-status")).toContainText("Connected");
    await expect(page.locator(".stale")).toHaveCount(0);
    await expect.poll(() => page.locator(".xterm-rows").textContent()).not.toBe(oldOutput);
    expect(await page.evaluate(() => (window as unknown as { socketCount: number }).socketCount)).toBe(count + 1);

    // With no lifecycle event, the scheduler still recovers by itself.
    await page.evaluate(() => (window as unknown as { phoneSocket: WebSocket }).phoneSocket.close());
    await expect(page.locator(".stale")).toBeVisible();
    await expect(page.locator(".connection-status")).toContainText("Connected");
    await expect(page.locator(".stale")).toHaveCount(0);
    expect(await page.evaluate(() => (window as unknown as { socketCount: number }).socketCount)).toBe(count + 2);

    // A resume without a prior hidden event must also replace a seemingly live socket.
    await page.evaluate(() => {
      window.dispatchEvent(new Event("pageshow"));
      window.dispatchEvent(new Event("online"));
      document.dispatchEvent(new Event("visibilitychange"));
    });
    await expect(page.locator(".stale")).toBeVisible();
    await expect(page.locator(".connection-status")).toContainText("Connected");
    await expect(page.locator(".stale")).toHaveCount(0);
    expect(await page.evaluate(() => (window as unknown as { socketCount: number }).socketCount)).toBe(count + 3);

    // A real relay refusal uses code 1008 with reason "revoked".
    expect((await request.post("/device-revoke", { data: {
      id: saved.machine, token: "22".repeat(32), phone: saved.phone,
    } })).ok()).toBeTruthy();
    await expect(page.getByRole("button", { name: "Pair again", exact: true })).toBeVisible();
    const stopped = await page.evaluate(() => (window as unknown as { socketCount: number }).socketCount);
    await page.evaluate(() => {
      window.dispatchEvent(new Event("online"));
      document.dispatchEvent(new Event("visibilitychange"));
      window.dispatchEvent(new Event("pageshow"));
    });
    await page.waitForTimeout(2200);
    expect(await page.evaluate(() => (window as unknown as { socketCount: number }).socketCount)).toBe(stopped);
    // A reload gets the relay's unknown-device refusal and also stops.
    await page.reload();
    await expect(page.getByRole("button", { name: "Pair again", exact: true })).toBeVisible();
    await page.waitForTimeout(2200);
    expect(await page.evaluate(() => (window as unknown as { socketCount: number }).socketCount)).toBe(1);
    await page.getByRole("button", { name: "Pair again", exact: true }).click();
    await expect(page.locator("#pairing")).toBeVisible();
    // Exercise the app's existing pairing-completed event with replacement credentials.
    const issued = await request.post("/device-token", { data: {
      id: saved.machine, token: "22".repeat(32), phone: saved.phone,
    } });
    expect(issued.ok()).toBeTruthy();
    const replacement = await issued.json() as { token: string };
    await page.evaluate(({ machine, token }) => {
      const key = `partyline.phone.${machine}`;
      const record = JSON.parse(localStorage.getItem(key)!);
      localStorage.setItem(key, JSON.stringify({ ...record, token }));
      window.dispatchEvent(new Event("pairing-cancel"));
    }, { machine: saved.machine, token: replacement.token });
    await expect(page.locator(".xterm-rows")).toContainText("fresh generation");
    await expect(page.getByRole("button", { name: "Pair again", exact: true })).toHaveCount(0);
  } finally {
    child.stdin.end();
    child.kill("SIGTERM");
  }
});

test("shell opens offline, excludes private responses and replaces older build caches", async ({ page, context }) => {
  let build = 1;
  const files = await readdir("dist");
  const server = createServer(async (req, res) => {
    const path = new URL(req.url!, "http://localhost").pathname;
    if (path === "/snapshot" || path === "/device-token") {
      res.setHeader("Content-Type", "application/json");
      res.end('{"token":"secret-token","snapshot":"private-output"}');
      return;
    }
    const file = path === "/" ? "index.html" : path.slice(1);
    if (!files.includes(file)) { res.writeHead(404).end(); return; }
    let body = await readFile(`dist/${file}`);
    if (file === "sw.js") body = Buffer.from(body.toString().replace("partyline-shell-", `partyline-shell-test-${build}-`));
    if (file === "index.html") body = Buffer.from(body.toString().replace("Partyline remote", `Partyline build ${build}`));
    res.setHeader("Content-Type", file.endsWith(".js") ? "text/javascript" : file.endsWith(".css") ? "text/css" : file.endsWith(".html") ? "text/html" : "application/octet-stream");
    res.setHeader("Cache-Control", "no-store");
    res.end(body);
  });
  await new Promise<void>(resolve => server.listen(0, "127.0.0.1", resolve));
  const address = server.address() as { port: number };
  const origin = `http://127.0.0.1:${address.port}`;
  try {
    await page.goto(origin);
    await page.evaluate(async () => {
      await navigator.serviceWorker.ready;
      if (!navigator.serviceWorker.controller) await new Promise<void>(resolve => navigator.serviceWorker.addEventListener("controllerchange", () => resolve(), { once: true }));
      await fetch("/snapshot");
      await fetch("/device-token", { method: "POST", body: "secret-token" });
      await fetch("/?token=secret-token");
    });
    const cached = await page.evaluate(async () => {
      const names = await caches.keys();
      const cache = await caches.open(names[0]);
      const requests = await cache.keys();
      return { names, urls: requests.map(r => new URL(r.url).pathname + new URL(r.url).search) };
    });
    expect(cached.names).toHaveLength(1);
    expect(cached.urls.sort()).toEqual(files.filter(f => f !== "sw.js").map(f => f === "index.html" ? "/" : `/${f}`).sort());
    await context.setOffline(true);
    await page.reload();
    await expect(page).toHaveTitle("Partyline build 1");
    await expect(page.locator("#pairing")).toBeVisible();
    await context.setOffline(false);
    build = 2;
    await page.evaluate(async () => {
      const registration = await navigator.serviceWorker.ready;
      await registration.update();
    });
    await expect.poll(() => page.evaluate(() => caches.keys())).toEqual([
      expect.stringContaining("partyline-shell-test-2-"),
    ]);
    await page.reload();
    await expect(page).toHaveTitle("Partyline build 2");
    await context.setOffline(true);
    await page.reload();
    await expect(page).toHaveTitle("Partyline build 2");
  } finally {
    await context.setOffline(false);
    await new Promise<void>((resolve, reject) => server.close(error => error ? reject(error) : resolve()));
  }
});
