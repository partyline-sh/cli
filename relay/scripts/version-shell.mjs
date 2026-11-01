import { createHash } from "node:crypto";
import { readdir, readFile, writeFile } from "node:fs/promises";

// Only public, build-produced shell files belong in this list.
const files = (await readdir("dist")).filter(file => file !== "sw.js").sort();
const hash = createHash("sha256");
for (const file of [...files, "sw.js"]) {
  hash.update(file);
  hash.update(await readFile(`dist/${file}`));
}
const source = await readFile("dist/sw.js", "utf8");
await writeFile("dist/sw.js", source
  .replace("__SHELL_VERSION__", hash.digest("hex").slice(0, 20))
  .replace('"__SHELL_FILES__"', JSON.stringify(files.map(file => file === "index.html" ? "/" : `/${file}`))));
