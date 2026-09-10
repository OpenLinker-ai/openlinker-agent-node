import assert from "node:assert/strict";
import { test } from "node:test";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { buildAgentNode } from "./build-agent-node.mjs";

test("the release builder injects the exact version into an executable binary", { timeout: 360_000 }, async () => {
  const root = await mkdtemp(join(tmpdir(), "agent-node-version-"));
  try {
    for (const version of ["dev", "v1.2.3-rc.4", "sha-0123456789ab"]) {
      const binary = join(root, process.platform === "win32" ? `${version}.exe` : version);
      // Run the same builder as the release workflow, using this test host's
      // architecture; cross-platform archive builds are a separate gate.
      const env = { ...process.env };
      delete env.GOOS;
      delete env.GOARCH;
      buildAgentNode(version, binary, { env });
      const result = spawnSync(binary, ["--version"], {
        cwd: root, env: process.platform === "win32" ? { SystemRoot: process.env.SystemRoot } : {},
        encoding: "utf8", timeout: 5000,
      });
      assert.equal(result.error, undefined);
      assert.equal(result.status, 0);
      assert.equal(result.stderr, "");
      assert.equal(result.stdout, `openlinker-agent-node/${version}\n`);
    }
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("invalid versions fail before any build can run", () => {
  for (const version of [undefined, "", "latest", "v1.2.3 extra", "openlinker-cli/v1.2.3", "sha-NOTHEX", "v1.2.3\n"]) {
    assert.throws(() => buildAgentNode(version, "unused", { env: { PATH: "" } }), /version must/);
  }
  assert.throws(() => buildAgentNode("dev", ""), /output path is required/);
});

test("the current candidate refuses tagged release until Core compatibility is delivered", async () => {
  const gate = fileURLToPath(new URL("./check-release-upgrade-readiness.mjs", import.meta.url));
  const result = spawnSync(process.execPath, [gate], { encoding: "utf8", timeout: 5000 });
  assert.equal(result.status, 1);
  assert.equal(result.stdout, "");
  assert.match(result.stderr, /^NODE_UPGRADE_COMPATIBILITY_NOT_READY:/);
  // This is wiring coverage, separate from the executable negative gate above.
  const workflow = await readFile(new URL("../.github/workflows/release.yml", import.meta.url), "utf8");
  const packaging = workflow.slice(workflow.indexOf("  package-binaries:"), workflow.indexOf("  publish-release:"));
  assert.match(packaging, /if: github\.ref_type == 'tag'\n\s+run: node scripts\/check-release-upgrade-readiness\.mjs/);
  assert.ok(packaging.includes("go test ./..."));
  assert.match(workflow, /needs: package-binaries/);
});
