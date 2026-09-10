#!/usr/bin/env node
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { statSync } from "node:fs";
import { resolve, join } from "node:path";
import { pathToFileURL } from "node:url";

export const nodeModule = "github.com/OpenLinker-ai/openlinker-agent-node";
const forbiddenModules = ["github.com/OpenLinker-ai/openlinker-plugin", "github.com/OpenLinker-ai/openlinker-cli"];
const leafForbidden = [...forbiddenModules, `${nodeModule}/internal`, `${nodeModule}/cmd`, "github.com/spf13/cobra"];
const platforms = ["linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64", "windows/arm64"];
const within = (value, prefix) => value === prefix || value.startsWith(`${prefix}/`);

// go list emits a stream of JSON objects, not an array. Parse structurally so
// escaped braces in values cannot truncate an object or hide a following one.
export function jsonObjects(text) {
  const objects = [];
  let start = -1, depth = 0, quoted = false, escaped = false;
  for (let i = 0; i < text.length; i++) {
    const ch = text[i];
    if (start === -1) {
      if (/\s/.test(ch)) continue;
      assert.equal(ch, "{", "invalid Go JSON stream");
      start = i;
    }
    if (quoted) {
      if (escaped) escaped = false;
      else if (ch === "\\") escaped = true;
      else if (ch === '"') quoted = false;
    } else if (ch === '"') quoted = true;
    else if (ch === "{") depth++;
    else if (ch === "}" && --depth === 0) {
      objects.push(JSON.parse(text.slice(start, i + 1)));
      start = -1;
    }
  }
  assert.equal(start, -1, "truncated Go JSON stream");
  assert.ok(objects.length > 0, "empty Go JSON stream");
  return objects;
}

function rejectModule(value) {
  assert.ok(typeof value === "string" && value.length > 0, "missing module path");
  const path = value.split("@")[0];
  assert.ok(!forbiddenModules.some((prefix) => within(path, prefix)), `forbidden module: ${path}`);
}

export function checkAdapterBoundaries(root, { targets = platforms, environment = {} } = {}) {
  assert.ok(statSync(join(root, "pkg/adapters")).isDirectory(), "missing adapter directory");
  assert.ok(targets.length > 0 && targets.every((target) => platforms.includes(target)), "invalid target selection");
  const env = { ...process.env, ...environment, GOWORK: "off", GOENV: "off", GOFLAGS: "", GOTOOLCHAIN: "local" };
  const go = (args, extra = {}) => execFileSync("go", args, {
    cwd: root, env: { ...env, ...extra }, encoding: "utf8", timeout: 120_000,
    maxBuffer: 64 * 1024 * 1024, stdio: ["ignore", "pipe", "pipe"],
  });
  const manifest = JSON.parse(go(["mod", "edit", "-json"]));
  assert.equal(manifest.Module?.Path, nodeModule, "unexpected Node module identity");
  assert.equal(manifest.Replace?.length ?? 0, 0, "release manifest must not contain replacements");
  for (const entry of manifest.Require ?? []) rejectModule(entry.Path);

  const graph = go(["mod", "graph"]).trim();
  assert.ok(graph, "empty module graph");
  for (const row of graph.split(/\r?\n/)) {
    const pair = row.trim().split(/\s+/);
    assert.equal(pair.length, 2, "invalid module graph edge");
    pair.forEach(rejectModule);
  }
  const modules = jsonObjects(go(["list", "-m", "-mod=readonly", "-json", "all"]));
  assert.ok(modules.some((entry) => entry.Path === nodeModule && entry.Main), "missing main module in build list");
  for (const entry of modules) {
    assert.ok(!entry.Error, `module resolution error: ${entry.Path}`);
    assert.ok(!entry.Replace, `resolved replacement: ${entry.Path}`);
    rejectModule(entry.Path);
  }

  const results = [];
  for (const target of targets) {
    const [GOOS, GOARCH] = target.split("/");
    // Public library consumers (and race tests) may enable cgo even though
    // shipped binaries disable it. Both selections must obey the same boundary.
    for (const CGO_ENABLED of ["0", "1"]) {
      const platform = { GOOS, GOARCH, CGO_ENABLED };
      for (const [pattern, denied] of [["./pkg/adapters/...", leafForbidden], ["./...", forbiddenModules]]) {
        const packages = jsonObjects(go(["list", "-deps", "-test", "-mod=readonly", "-json", pattern], platform));
        assert.ok(packages.some((entry) => within(entry.ImportPath ?? "", `${nodeModule}/pkg/adapters`)), "no adapter packages resolved");
        for (const entry of packages) {
          assert.ok(!entry.Error && !entry.DepsErrors?.length && !entry.Incomplete, `unresolved package: ${entry.ImportPath}`);
          assert.ok(typeof entry.ImportPath === "string", "missing package identity");
          assert.ok(!denied.some((prefix) => within(entry.ImportPath, prefix)), `forbidden package: ${entry.ImportPath}`);
          if (entry.Module) rejectModule(entry.Module.Path);
        }
        results.push({ target, cgo_enabled: CGO_ENABLED, scope: pattern, packages: packages.length });
      }
    }
  }
  return { scope: "isolated_module_graph_and_packages", gowork: "off", modules: modules.length, results };
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try {
    assert.equal(process.argv.length, 2, "usage: node scripts/check-adapter-boundaries.mjs");
    console.log(JSON.stringify(checkAdapterBoundaries(resolve(import.meta.dirname, "..")), null, 2));
  } catch (error) {
    // No environment or source payload is printed on diagnostic failures.
    console.error(`Adapter boundary check failed (${error.name}): ${error.message}`);
    process.exitCode = 1;
  }
}
