// An 8 KiB terminal input frame is at most 10,924 base64 characters. Leave room
// for the small JSON envelope, but reject large bridge messages before parsing.
const maxBridgeCharacters = 12 * 1024;
export function terminalBridgeMessage(raw: string): Record<string, unknown> {
  if (typeof raw !== 'string' || raw.length > maxBridgeCharacters)
    throw Error('Terminal message exceeded its limit');
  const message: unknown = JSON.parse(raw);
  if (!message || typeof message !== 'object' || Array.isArray(message))
    throw Error('Invalid terminal message');
  return message as Record<string, unknown>;
}
