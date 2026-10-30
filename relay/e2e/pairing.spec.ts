import {
  test,
  expect,
  type APIRequestContext,
  type Page,
} from "@playwright/test";
import { createHash, randomBytes } from "node:crypto";
import { WebSocket } from "ws";
import { x25519 } from "@noble/curves/ed25519";
import { HandshakeState } from "../src/noise";
import { dataFrame } from "../src/pairing";

const origin = "http://localhost:8787";
const PHOTO =
  "iVBORw0KGgoAAAANSUhEUgAAAQAAAAEAAQMAAABmvDolAAAABlBMVEX///8AAABVwtN+AAABVUlEQVR42uyYsW0FIRBEFxEQbgmUQmsujVIogZDgxFgLy/ed5XMBLBN8fXEvGrHDAB0dHR39JwxdRKGFKgtxrnztBXT58ZdvoRFX4hKLrCRzgAMuf8lfNapEIFsFPNACKlfjAIWmC5sCOhdiAwDx4e/B2RzQnJR8CJUrl5cg3R1YCsAIyldtDkybIPHQiAGUWFJOeTNgzAWNgJDjgFEoIqdf9WB/wHVyOhfEqKMm5ft+MAKgO1w6GmJUkaBMN8YIQOQ+9aDyOi/SPSd3AAB0r0ZIPozP+RmDFoAuTmHkJFSF0rMmmQAkHcgPhLhKTspYGAS603MTqDytemyYLQDVTxskivm+H4wAqxUvp0qcvdgaoLekeSyqUSaB9Tggt2Zek7MvMAm8vYmZAVpo4y4YUVI2CHzeSMUprjIU98uiFeDzSEIUMNvBsx7sARwdHVnUdwAAAP//yUVQYUg+NBEAAAAASUVORK5CYII="; // a QR code of a PTLN-PAIR string
const id = "11".repeat(16),
  token = "22".repeat(32);
const fingerprint = (key: Uint8Array) =>
  createHash("sha256")
    .update(key)
    .digest("hex")
    .slice(0, 32)
    .match(/.{4}/g)!
    .join(" ");
async function authenticator(page: Page) {
  const cdp = await page.context().newCDPSession(page);
  await cdp.send("WebAuthn.enable");
  await cdp.send("WebAuthn.addVirtualAuthenticator", {
    options: {
      protocol: "ctap2",
      transport: "internal",
      hasResidentKey: true,
      hasUserVerification: true,
      isUserVerified: true,
      automaticPresenceSimulation: true,
    },
  });
}
async function withMachine(
  request: APIRequestContext,
  run: (
    typed: string,
    machineFingerprint: string,
    approve: (yes: boolean) => void,
    link: string,
  ) => Promise<void>,
) {
  // The keys derive from the link's secret, as pairDerive does in Go.
  const secret = randomBytes(16);
  const derive = (label: string) =>
    createHash("sha256")
      .update(Buffer.concat([Buffer.from(`ptln pair ${label}\0`), secret]))
      .digest();
  const route = derive("route").subarray(0, 16).toString("hex"),
    invite = derive("invite").toString("hex");
  const psk = derive("psk"),
    key = randomBytes(32),
    challenge = randomBytes(32);
  const response = await request.post("/device-invite", {
    data: {
      id,
      token,
      route,
      hash: createHash("sha256").update(invite).digest("hex"),
    },
  });
  expect(response.ok()).toBe(true);
  const { expires } = await response.json();
  const machinePublic = x25519.getPublicKey(key);
  const link = `${origin}/#p=${Buffer.concat([Buffer.from(machinePublic), secret]).toString("base64url")}`;
  const typed =
    "PTLN-PAIR " +
    JSON.stringify({
      relay: origin,
      machine: id,
      route,
      invite,
      psk: psk.toString("hex"),
      public: Buffer.from(machinePublic).toString("hex"),
      expires,
    });
  const ws = new WebSocket(origin.replace("http", "ws") + "/connect");
  const responder = new HandshakeState({
    pattern: "IKpsk2",
    initiator: false,
    staticPrivate: key,
    psk,
  });
  let ready!: () => void, approve!: (yes: boolean) => void;
  const acknowledged = new Promise<void>((resolve) => {
    ready = resolve;
  });
  const decision = new Promise<boolean>((resolve) => {
    approve = resolve;
  });
  let stage = 0;
  const failed = new Promise<never>((_, reject) => {
    ws.on("error", reject);
    ws.on("open", () =>
      ws.send(
        dataFrame(
          id,
          new TextEncoder().encode(
            JSON.stringify({ role: "machine", token, min: 1, max: 1 }),
          ),
          1,
        ),
      ),
    );
    ws.on("message", async (raw) => {
      try {
        const b = new Uint8Array(raw as Buffer);
        if (b[0] === 6 || b[0] === 4) return;
        if (b[0] === 1) {
          ready();
          return;
        }
        expect(b[0]).toBe(2);
        expect(Buffer.from(b).includes(psk)).toBe(false);
        if (stage === 0) {
          stage = 1;
          responder.readMessage(b.slice(17));
          ws.send(
            dataFrame(
              route,
              responder.writeMessage(
                new TextEncoder().encode(
                  JSON.stringify({ challenge: challenge.toString("base64") }),
                ),
              ),
            ),
          );
        } else {
          expect(stage).toBe(1);
          stage = 2;
          const payload = JSON.parse(
            new TextDecoder().decode(
              responder.split().receive.decrypt(b.slice(17)),
            ),
          );
          // A name and the phone's key; no passkey.
          expect(typeof payload.name).toBe("string");
          expect(payload.name.length).toBeGreaterThan(0);
          expect(payload.registration).toBeUndefined();
          expect(Buffer.from(payload.static, "base64")).toEqual(
            Buffer.from(responder.peerStatic!),
          );
          const approved = await decision;
          let device = "";
          if (approved) {
            const issued = await request.post("/device-token", {
              data: { id, token, phone: route },
            });
            expect(issued.ok()).toBe(true);
            device = (await issued.json()).token;
          }
          ws.send(
            dataFrame(
              route,
              responder.split().send.encrypt(
                new TextEncoder().encode(
                  JSON.stringify({
                    approved,
                    token: device,
                    machine: id,
                    phone: route,
                    fingerprint: fingerprint(responder.peerStatic!),
                  }),
                ),
              ),
            ),
          );
        }
      } catch (error) {
        reject(error);
      }
    });
  });
  try {
    await Promise.race([acknowledged, failed]);
    await Promise.race([
      run(typed, fingerprint(machinePublic), approve, link),
      failed,
    ]);
  } finally {
    ws.close();
    await request.post("/device-revoke", { data: { id, token, phone: route } });
  }
}

// The Home Screen app: pairing is offered there, not in a Safari tab.
async function installedApp(page: import("@playwright/test").Page) {
  await page.addInitScript(() =>
    Object.defineProperty(navigator, "standalone", { value: true }),
  );
}

test("a camera that is refused says why and offers a photo; a photo without a code says so", async ({
  page,
}) => {
  await installedApp(page);
  await page.addInitScript(() => {
    navigator.mediaDevices.getUserMedia = () =>
      Promise.reject(new DOMException("denied", "NotAllowedError"));
  });
  await page.goto("/");
  await page.getByRole("button", { name: "Or scan it here" }).click();
  await expect(page.getByRole("status")).toContainText("Camera access is off");
  await expect(page.getByRole("status")).toContainText("Take a photo");
  // A blank white PNG: read, and no code found in it.
  const png = Buffer.from(
    "iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAIAAABLbSncAAAAD0lEQVR4nGP4jwMwDC0JALoev0Ewkwr8AAAAAElFTkSuQmCC",
    "base64",
  );
  await page
    .locator("#photo")
    .setInputFiles({ name: "code.png", mimeType: "image/png", buffer: png });
  await expect(page.getByRole("status")).toContainText(
    "No pairing code in that photo",
  );
});
test("a photo of a pairing code starts pairing at once", async ({ page }) => {
  await page.goto("/");
  await page.locator("#photo").setInputFiles({
    name: "code.png",
    mimeType: "image/png",
    buffer: Buffer.from(PHOTO, "base64"),
  });
  // The photo's code is not a live offer, so pairing starts and then says to scan again.
  await expect(page.getByRole("status")).toContainText("again");
});
test("opening the link shows the code at once and pairs when partyline approves", async ({
  page,
  request,
}) => {
  await page.addInitScript(() => Object.defineProperty(navigator, "userAgent", {
    value: "Mozilla/5.0 (iPhone) AppleWebKit/605.1.15 Version/18.0 Mobile Safari/604.1",
  }));
  await withMachine(request, async (_typed, _fingerprint, approve, link) => {
    await page.goto(link);
    await expect(page.getByRole("dialog")).not.toBeVisible();
    await expect(page.locator("#paircode")).toHaveText(/^\d{4}$/);
    expect(await page.evaluate(() => location.hash)).toBe("");
    await expect(page.getByRole("status")).toContainText("press ⏎");
    expect(await page.evaluate(() => localStorage.length)).toBe(0);
    approve(true);
    await expect(page.getByRole("heading", { name: "Paired" })).toBeVisible();
    const saved = await page.evaluate(
      (id) => JSON.parse(localStorage.getItem(`partyline.phone.${id}`)!),
      id,
    );
    expect(saved.token).toMatch(/^[a-f0-9]{64}$/);
  });
});
test("scanning again while the app is open pairs too", async ({
  page,
  request,
}) => {
  await page.goto("/");
  await expect(
    page.getByRole("heading", { name: "Pair with your computer" }),
  ).toBeVisible();
  await withMachine(request, async (_typed, _fingerprint, approve, link) => {
    // Only the part after # changes: the page does not reload.
    await page.evaluate(
      (hash) => (location.hash = hash),
      link.slice(link.indexOf("#")),
    );
    await expect(page.locator("#paircode")).toHaveText(/^\d{4}$/);
    approve(true);
    await expect(page.getByRole("heading", { name: "Paired" })).toBeVisible();
  });
});
test("a pasted code pairs the same way", async ({ page, request }) => {
  await withMachine(request, async (typed, _fingerprint, approve) => {
    await page.goto("/");
    await page
      .getByRole("button", { name: "Can't scan? Paste the code instead" })
      .click();
    await page.locator("textarea").fill(typed);
    await page.getByRole("button", { name: "Use this code" }).click();
    await expect(page.locator("#paircode")).toHaveText(/^\d{4}$/);
    approve(true);
    await expect(page.getByRole("heading", { name: "Paired" })).toBeVisible();
  });
});
test("refused in partyline saves nothing and says to scan again", async ({
  page,
  request,
}) => {
  await withMachine(request, async (_typed, _fingerprint, approve, link) => {
    await page.goto(link);
    await expect(page.locator("#paircode")).toHaveText(/^\d{4}$/);
    approve(false);
    await expect(page.getByRole("status")).toContainText("refused");
    await expect(page.getByRole("status")).toContainText("again");
    expect(await page.evaluate(() => localStorage.length)).toBe(0);
  });
});
