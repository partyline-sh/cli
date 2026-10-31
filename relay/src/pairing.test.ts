import { describe, expect, it } from "vitest";
import { fromLink, pairCode } from "./pairing";

// Pinned to the same vectors as remote_pair_link_test.go.
describe("pairing link and code", () => {
  it("reads the Go pairing link and derives the same keys", async () => {
    const typed = await fromLink(
      "https://relay.test/#p=IiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIzMzMzMzMzMzMzMzMzMzMz",
    );
    expect(JSON.parse(typed.slice("PTLN-PAIR ".length))).toEqual({
      relay: "https://relay.test",
      public: "22".repeat(32),
      psk: "bc9ab9bf462be380ab6ba5a4ba213a3cc1267966156a419a1c707ed4320cc62d",
      invite:
        "0ce907f7ca87a8fbd2bac6f7f99fb6adef1c5be0d281367dcc4af1694530c7e7",
      route: "ac93c285392f9037ac8b833db070c830",
    });
    expect(await fromLink("https://relay.test/#p=AAAA")).toBe("");
    expect(await fromLink("hello")).toBe("");
  });
  it("shows the same code as partyline", async () => {
    expect(await pairCode(Uint8Array.from({ length: 32 }, (_, i) => i))).toBe(
      "2492",
    );
  });
});
