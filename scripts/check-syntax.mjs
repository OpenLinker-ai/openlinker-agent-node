import { spawnSync } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";

const root = new URL("..", import.meta.url).pathname;
const dirs = ["bin", "scripts", "src", "test"];
const files = [];

for (const dir of dirs) {
  await collect(path.join(root, dir), files);
}

for (const file of files) {
  const result = spawnSync(process.execPath, ["--check", file], {
    cwd: root,
    encoding: "utf8",
  });
  if (result.status !== 0) {
    process.stderr.write(result.stderr);
    process.exit(result.status ?? 1);
  }
}

async function collect(dir, out) {
  let entries = [];
  try {
    entries = await fs.readdir(dir, { withFileTypes: true });
  } catch {
    return;
  }
  for (const entry of entries) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      await collect(full, out);
    } else if (entry.isFile() && entry.name.endsWith(".mjs")) {
      out.push(full);
    }
  }
}
