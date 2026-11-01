// Cache only the build-versioned app shell, network first with offline fallback.
// Never cache snapshots, keys, tokens, or relay responses.
const CACHE = "partyline-shell-0c1a2c8311ad365e2e44";
const SHELL = ["/app.css","/app.js","/icon-192.png","/icon-512.png","/","/layout.css","/manifest.webmanifest","/pairing.js","/session.js","/theme.css"];
self.addEventListener("install", (event) => {
  event.waitUntil((async () => {
    const cache = await caches.open(CACHE);
    await cache.addAll(SHELL);
    await self.skipWaiting();
  })());
});
self.addEventListener("activate", (event) => {
  event.waitUntil((async () => {
    for (const name of await caches.keys())
      if (name !== CACHE) await caches.delete(name);
    await self.clients.claim();
  })());
});
self.addEventListener("fetch", (event) => {
  const url = new URL(event.request.url);
  // Query strings (including pairing links) and every API route are network only.
  if (event.request.method !== "GET" || url.origin !== self.location.origin ||
      url.search || !SHELL.includes(url.pathname)) return;
  event.respondWith((async () => {
    try {
      const response = await fetch(event.request, { cache: "no-store" });
      if (!response.ok) throw new Error("Shell unavailable");
      const cache = await caches.open(CACHE);
      await cache.put(url.pathname, response.clone());
      return response;
    } catch (error) {
      const cached = await (await caches.open(CACHE)).match(url.pathname);
      if (cached) return cached;
      throw error;
    }
  })());
});
