import type { InnerMessage } from "./session";
const encoder = new TextEncoder();

export function encodeMessages(messages: InnerMessage[]): Uint8Array {
  for (const m of messages) {
    if (m.type !== "input" && m.type !== "chat_send") continue;
    const data = m.type === "chat_send" ? m.text : m.data;
    if (typeof data !== "string") throw new Error("Data must be text");
    if (encoder.encode(data).length > 24 * 1024) throw new Error("Payload exceeds 24 KiB");
    if (/[\x00-\x08\x0b-\x1f\x7f-\x9f]/u.test(data)) throw new Error("Control character refused");
  }
  // Match Go's JSON escaping, and check before encryption consumes a nonce.
  const json = JSON.stringify(messages).replace(/[<>&\u2028\u2029]/g, c => "\\u" + c.charCodeAt(0).toString(16).padStart(4, "0"));
  const plain = encoder.encode(json);
  if (plain.length + 16 > 65535) throw new Error("Encoded input exceeds transport frame");
  return plain;
}
