// Builds the web UI into the folder the Go server embeds. Run from the repo
// root with `make web`, or directly: `bun web/build.ts`.
import { rm } from "node:fs/promises";
import { join } from "node:path";

const root = import.meta.dir;
const out = join(root, "../server/internal/webui/dist");

await rm(out, { recursive: true, force: true });

const builds = [
  // The page, its CSS and fonts, with hashed names.
  Bun.build({ entrypoints: [join(root, "index.html")], outdir: out, minify: true, target: "browser" }),
  // The measurement worker under a fixed name, since main.ts loads it by URL.
  Bun.build({ entrypoints: [join(root, "src/engine.ts")], outdir: out, naming: "engine.js", minify: true, target: "browser", format: "iife" }),
];
for (const result of await Promise.all(builds)) {
  if (!result.success) {
    for (const log of result.logs) console.error(log);
    process.exit(1);
  }
  for (const o of result.outputs) console.log(`${(o.size / 1024).toFixed(1).padStart(7)} KiB  ${o.path.slice(out.length + 1)}`);
}
