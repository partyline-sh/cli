import { describe, expect, it } from "vitest";
import { clientAddress } from "./client-address";

describe("relay client address buckets", () => {
  it.each([
    [null, "shared"], ["invalid", "shared"], ["198.51.100.1", "198.51.100.1"],
    ["999.1.2.3", "shared"], ["2001:db8:1:2::1", "2001:db8:1:2::/64"],
    ["2001:db8:0001:0002:ffff:ffff:ffff:ffff", "2001:db8:1:2::/64"],
    ["2001:db8:1:3::1", "2001:db8:1:3::/64"],
    ["::ffff:198.51.100.1", "198.51.100.1"], ["::1", "0:0:0:0::/64"],
  ])("normalizes %s", (address, bucket) => {
    expect(clientAddress(address)).toBe(bucket);
  });
});
