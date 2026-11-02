import { test, expect } from "@playwright/test";
import { readdir, readFile } from "node:fs/promises";

test("Worker serves every built PWA asset byte for byte", async ({ request }) => {
  for (const file of await readdir("dist")) {
    // Workers redirects index.html to the canonical root URL.
    const response = await request.get(file === "index.html" ? "/" : `/${file}`);
    expect(response.status(), file).toBe(200);
    expect(response.headers()["content-security-policy"], file).toBe(
      "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'",
    );
    expect(await response.body(), file).toEqual(await readFile(`dist/${file}`));
  }
});
