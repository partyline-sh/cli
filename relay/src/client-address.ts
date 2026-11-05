// Cloudflare supplies CF-Connecting-IP at the edge. A missing/invalid address
// shares one conservative local-development bucket. IPv6 clients share a /64,
// so rotating interface addresses does not create new handshake allowances.
export function clientAddress(ip: string | null): string {
  if (!ip || ip.length > 64) return "shared";
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(ip)) {
    const bytes = ip.split(".").map(Number);
    return bytes.every(b => b <= 255) ? bytes.join(".") : "shared";
  }
  try {
    const host = new URL(`http://[${ip}]/`).hostname.slice(1, -1);
    const [left, right] = host.split("::");
    const prefix = left ? left.split(":") : [];
    const suffix = right ? right.split(":") : [];
    const words = right !== undefined
      ? [...prefix, ...Array(8 - prefix.length - suffix.length).fill("0"), ...suffix]
      : prefix;
    if (words.length !== 8) return "shared";
    if (words.slice(0, 5).every(w => Number.parseInt(w, 16) === 0) && words[5] === "ffff") {
      const hi = Number.parseInt(words[6], 16), lo = Number.parseInt(words[7], 16);
      return [hi >> 8, hi & 255, lo >> 8, lo & 255].join(".");
    }
    return words.slice(0, 4).map(w => Number.parseInt(w, 16).toString(16)).join(":") + "::/64";
  } catch {
    return "shared";
  }
}
