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
  ) => Promise<void>,
) {
  const route = randomBytes(16).toString("hex"),
    invite = randomBytes(32).toString("hex");
  const psk = randomBytes(32),
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
          expect(payload.name).toBe("Test iPhone");
          expect(Buffer.from(payload.static, "base64")).toEqual(
            Buffer.from(responder.peerStatic!),
          );
          const client = JSON.parse(
            Buffer.from(payload.assertion.clientData, "base64").toString(),
          );
          expect(client.type).toBe("webauthn.get");
          expect(client.origin).toBe(origin);
          expect(client.challenge).toBe(challenge.toString("base64url"));
          const auth = Buffer.from(
            payload.assertion.authenticatorData,
            "base64",
          );
          expect(auth.subarray(0, 32)).toEqual(
            createHash("sha256").update("localhost").digest(),
          );
          expect(auth[32] & 5).toBe(5);
          expect(payload.registration.id).toBe(payload.assertion.id);
          expect(
            Buffer.from(payload.assertion.signature, "base64").length,
          ).toBeGreaterThan(60);
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
      run(typed, fingerprint(machinePublic), approve),
      failed,
    ]);
  } finally {
    ws.close();
    await request.post("/device-revoke", { data: { id, token, phone: route } });
  }
}

test("pairing saves only after simulated popup approval and shows both fingerprints", async ({
  page,
  request,
}) => {
  await authenticator(page);
  await withMachine(request, async (typed, machineFingerprint, approve) => {
    await page.goto("/");
    await page.locator("#name").fill("Test iPhone");
    await page.locator("textarea").fill(typed);
    await page.getByRole("button", { name: "Pair", exact: true }).click();
    await expect(page.getByRole("status")).toContainText("then approve");
    await expect(page.getByRole("status")).toContainText(machineFingerprint);
    expect(await page.evaluate(() => localStorage.length)).toBe(0);
    approve(true);
    await expect(page.getByRole("status")).toContainText("Paired.");
    await expect(page.getByRole("status")).toContainText(machineFingerprint);
    const saved = await page.evaluate(
      (id) => JSON.parse(localStorage.getItem(`partyline.phone.${id}`)!),
      id,
    );
    expect(saved.credential).toBeTruthy();
    expect(saved.token).toMatch(/^[a-f0-9]{64}$/);
    expect(saved.machineFingerprint).toBe(machineFingerprint);
  });
});
test("refused popup approval saves no browser pairing", async ({
  page,
  request,
}) => {
  await authenticator(page);
  await withMachine(request, async (typed, _fingerprint, approve) => {
    await page.goto("/");
    await page.locator("#name").fill("Test iPhone");
    await page.locator("textarea").fill(typed);
    await page.getByRole("button", { name: "Pair", exact: true }).click();
    await expect(page.getByRole("status")).toContainText("then approve");
    approve(false);
    await expect(page.getByRole("status")).toContainText("Pairing refused");
    expect(await page.evaluate(() => localStorage.length)).toBe(0);
  });
});
