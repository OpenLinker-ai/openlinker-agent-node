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

const releaseGate = fileURLToPath(new URL("./check-release-upgrade-readiness.mjs", import.meta.url));

function checkRelease(args, env = process.env) {
  const result = spawnSync(process.execPath, [releaseGate, ...args], {
    env, encoding: "utf8", timeout: 5000,
  });
  assert.equal(result.error, undefined);
  return result;
}

test("the executable release gate allows only canonical pre-1.0 test prereleases", () => {
  for (const tag of ["v0.0.0-alpha.0", "v0.1.59-alpha.1", "v0.1.59-beta.2", "v0.1.59-rc.1", "v0.20.100-rc.10"]) {
    const result = checkRelease([tag]);
    assert.equal(result.status, 0, tag);
    assert.equal(result.stderr, "", tag);
    assert.match(result.stdout, /^NODE_TEST_PRERELEASE_ALLOWED:/, tag);
  }
});

test("the executable release gate rejects missing, stable, production and malformed versions", () => {
  for (const args of [
    [], [""], ["v0.1.59"], ["v1.0.0"], ["v1.0.0-rc.1"], ["v2.0.0-alpha.1"],
    ["dev"], ["latest"], ["sha-0123456789ab"], ["0.1.59-rc.1"], ["refs/tags/v0.1.59-rc.1"],
    ["v0.01.59-rc.1"], ["v0.1.059-rc.1"], ["v0.1.59-rc.01"], ["v00.1.59-rc.1"],
    ["v0.1.59-preview.1"], ["v0.1.59-RC.1"], ["v0.1.59-rc"], ["v0.1.59-rc.-1"],
    ["v0.1.59-rc.1.2"], ["v0.1.59-rc.1+build.1"], ["v0.1.59-rc.1\n"], [" v0.1.59-rc.1"],
    ["v0.1.59-rc.1", "extra"],
  ]) {
    const result = checkRelease(args);
    assert.equal(result.status, 1, JSON.stringify(args));
    assert.equal(result.stdout, "", JSON.stringify(args));
    assert.match(result.stderr, /^NODE_TEST_PRERELEASE_REQUIRED:/, JSON.stringify(args));
  }
});

test("environment values cannot replace or bypass the explicit release tag", () => {
  const env = {
    ...process.env,
    GITHUB_REF_NAME: "v0.1.59-rc.1",
    NODE_UPGRADE_COMPATIBILITY_READY: "true",
    ALLOW_NODE_UPGRADE_WITHOUT_CORE: "1",
  };
  for (const args of [[], ["v0.1.59"], ["v1.0.0-rc.1"]]) {
    const result = checkRelease(args, env);
    assert.equal(result.status, 1);
    assert.equal(result.stdout, "");
  }
});

test("release workflow gates the actual tag and keeps tested six-platform prerelease packaging", async () => {
  // Wiring coverage, distinct from the real gate and binary executions above.
  const workflow = await readFile(new URL("../.github/workflows/release.yml", import.meta.url), "utf8");
  const packaging = workflow.slice(workflow.indexOf("  package-binaries:"), workflow.indexOf("  publish-release:"));
  assert.match(packaging, /if: github\.ref_type == 'tag'\n\s+run: node scripts\/check-release-upgrade-readiness\.mjs "\$GITHUB_REF_NAME"/);
  assert.ok(packaging.indexOf("check-release-upgrade-readiness.mjs") < packaging.indexOf("go mod download"));
  assert.ok(packaging.includes("go test ./..."));
  assert.ok(packaging.indexOf("go test ./...") < packaging.indexOf("- name: Package binary"));
  assert.ok(packaging.includes("node --test scripts/build-agent-node.test.mjs"));
  assert.deepEqual([...packaging.matchAll(/- goos: (\w+)\n\s+goarch: (\w+)/g)].map((match) => `${match[1]}/${match[2]}`), [
    "linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64", "windows/arm64",
  ]);
  assert.ok(packaging.includes('version="sha-${GITHUB_SHA::12}"'));
  assert.ok(packaging.includes("sha256sum"));
  assert.ok(packaging.includes(".sha256"));
  assert.match(workflow, /publish-release:[\s\S]*?if: github\.ref_type == 'tag'/);
  assert.match(workflow, /needs: package-binaries/);
  assert.match(workflow, /gh release create "\$\{tag\}"[^\n]*--prerelease/);
  assert.match(workflow, /gh release edit "\$\{tag\}"[^\n]*--prerelease/);
});
