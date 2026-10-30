import { test } from "node:test";
import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
import { runInNewContext } from "node:vm";

const origin = "https://phone.test";
const source = await readFile("dist/sw.js", "utf8");
function worker(script = source) {
  const handlers = {};
  const stores = new Map();
  let online = true;
  const key = request => new URL(typeof request === "string" ? request : request.url, origin).href;
  const caches = {
    async keys() { return [...stores.keys()]; },
    async delete(name) { return stores.delete(name); },
    async open(name) {
      if (!stores.has(name)) stores.set(name, new Map());
      const store = stores.get(name);
      return {
        async addAll(paths) { for (const path of paths) store.set(key(path), new Response("shell " + path)); },
        async put(request, response) { store.set(key(request), response); },
        async match(request) { return store.get(key(request))?.clone(); },
      };
    },
  };
  runInNewContext(script, {
    URL, caches,
    fetch: async () => {
      if (!online) throw new Error("offline");
      return new Response("fresh shell");
    },
    self: {
      location: { origin },
      skipWaiting: async () => {},
      clients: { claim: async () => {} },
      addEventListener: (name, handler) => handlers[name] = handler,
    },
  });
  return {
    caches,
    setOnline(value) { online = value; },
    async lifecycle(name) { let task; handlers[name]({ waitUntil(promise) { task = promise; } }); await task; },
    request(path, method = "GET") {
      let result;
      handlers.fetch({
        request: new Request(new URL(path, origin), { method }),
        respondWith(response) { result = response; },
      });
      return result;
    },
  };
}

test("built worker caches exactly the shell, uses network first and falls back offline", async () => {
  const sw = worker();
  await sw.lifecycle("install");
  const names = await sw.caches.keys();
  assert.equal(names.length, 1);
  assert.match(names[0], /^partyline-shell-[a-f0-9]{20}$/);
  assert.equal(await (await sw.request("/")).text(), "fresh shell");
  sw.setOnline(false);
  assert.equal(await (await sw.request("/")).text(), "fresh shell");
  for (const file of await readdir("dist")) {
    if (file === "sw.js") continue;
    assert.ok(await sw.request(file === "index.html" ? "/" : "/" + file), file);
  }
  for (const path of ["/connect", "/snapshot", "/device-token", "/?token=secret", "/app.js?token=secret", "https://other.test/app.js"]) {
    assert.equal(sw.request(path), undefined, path);
  }
  assert.equal(sw.request("/", "POST"), undefined);
});

test("activate deletes every older cache", async () => {
  const sw = worker();
  await sw.caches.open("partyline-shell-previous");
  await sw.caches.open("legacy-cache");
  await sw.lifecycle("install");
  await sw.lifecycle("activate");
  const names = await sw.caches.keys();
  assert.equal(names.length, 1);
  assert.match(names[0], /^partyline-shell-[a-f0-9]{20}$/);
});
