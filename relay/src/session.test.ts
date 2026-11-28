import { describe, it, expect } from "vitest";
import { PhoneSession, type InnerMessage } from "./session";

describe("transcript revision recovery", () => {
  it("requests a full resync on a gap and keeps the displayed snapshot until it arrives", () => {
    const delivered: InnerMessage[] = [];
    const sent: InnerMessage[][] = [];
    // Exercise delivery without opening sockets or constructing browser timers.
    const session = Object.assign(Object.create(PhoneSession.prototype), {
      transcriptRevisions: new Map(), resyncing: new Set(),
      receive: (m: InnerMessage) => delivered.push(m),
      send: (ms: InnerMessage[]) => sent.push(ms),
    }) as { deliver(ms: InnerMessage[]): void };
    const message = (revision: number): InnerMessage => ({
      type: "transcript:%1", revision, data: [{ role: "assistant", text: `revision ${revision}`, tools: [] }],
    });
    session.deliver([message(1)]);
    session.deliver([message(3)]);
    expect(delivered).toEqual([message(1)]);
    expect(sent).toEqual([[{ type: "resync", pane: "transcript:%1" }]]);
    session.deliver([message(4)]);
    expect(delivered).toEqual([message(1), message(4)]);
    session.deliver([message(5)]);
    expect(sent).toHaveLength(1);
    expect(delivered.at(-1)).toEqual(message(5));
  });
});
