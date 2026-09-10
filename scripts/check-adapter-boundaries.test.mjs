import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtemp, mkdir, writeFile, rm, rmdir } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { pathToFileURL } from "node:url";
import test from "node:test";
import { checkAdapterBoundaries, jsonObjects, nodeModule } from "./check-adapter-boundaries.mjs";

test("Go JSON streams are nonempty, complete and structurally parsed", () => {
  assert.deepEqual(jsonObjects(' {"Path":"brace } and \\\" quote"}\n{"Main":true}'), [{Path: 'brace } and " quote'}, {Main: true}]);
  for (const value of ["", " ", "{}garbage", '{"Path":', "[]", "{} {}x"]) assert.throws(() => jsonObjects(value));
});

test("real isolated Go graphs reject direct, transitive, replacement, host and platform contamination", { timeout: 240_000 }, async (t) => {
  const fixture = await mkdtemp(join(tmpdir(), "openlinker-adapter-boundaries-"));
  t.after(() => rm(fixture, { recursive: true, force: true }));
  const root = join(fixture, "node");
  async function file(path, contents) {
    await mkdir(dirname(path), { recursive: true });
    await writeFile(path, contents);
  }
  const plugin = "github.com/OpenLinker-ai/openlinker-plugin";
  const cli = "github.com/OpenLinker-ai/openlinker-cli";
  const leaf = "example.invalid/leaf";
  const middle = "example.invalid/middle";
  const cobra = "github.com/spf13/cobra";
  const environment = {
    GOPROXY: `${pathToFileURL(join(fixture, "proxy")).href},off`, GOSUMDB: "off",
    GOPRIVATE: "", GONOPROXY: "none", GONOSUMDB: "none",
    GOMODCACHE: join(fixture, "mod-cache"), GOCACHE: join(fixture, "build-cache"),
    GOWORK: join(fixture, "go.work"),
  };
  // Real immutable file-proxy fixtures, not checker mocks or release replaces.
  async function module(path, requires = "") {
    const mod = `module ${path}\n\ngo 1.23.0\n${requires}\n`;
    const escaped = path.replace(/[A-Z]/g, (char) => `!${char.toLowerCase()}`);
    const prefix = `${path}@v1.0.0`;
    const archiveRoot = join(fixture, "archives", escaped);
    const proxyRoot = join(fixture, "proxy", escaped, "@v");
    await file(join(archiveRoot, prefix, "go.mod"), mod);
    await file(join(archiveRoot, prefix, "value.go"), "package value\nconst Value = 1\n");
    await file(join(proxyRoot, "v1.0.0.mod"), mod);
    await file(join(proxyRoot, "v1.0.0.info"), JSON.stringify({Version: "v1.0.0", Time: "2026-01-01T00:00:00Z"}));
    execFileSync("zip", ["-q", "-r", join(proxyRoot, "v1.0.0.zip"), prefix], { cwd: archiveRoot, timeout: 10_000 });
  }
  for (const path of [leaf, plugin, `${plugin}/nested`, cli, cobra]) await module(path);
  await module(middle, `require ${plugin} v1.0.0`);
  async function manifest(extra = "") {
    await file(join(root, "go.mod"), `module ${nodeModule}\n\ngo 1.23.0\nrequire ${leaf} v1.0.0\n${extra}\n`);
  }
  async function source(imports = `import _ "${leaf}"`) {
    await file(join(root, "pkg/adapters/value.go"), `package adapters\n${imports}\nconst Value = 1\n`);
  }
  function download() {
    execFileSync("go", ["mod", "download", "all"], {
      cwd: root, env: {...process.env, ...environment, GOWORK: "off", GOENV: "off", GOTOOLCHAIN: "local", GOFLAGS: "-modcacherw"},
      timeout: 30_000, stdio: "pipe",
    });
  }
  const check = (targets = ["linux/amd64"]) => checkAdapterBoundaries(root, {targets, environment});
  await manifest();
  await source();
  await file(join(fixture, "unrelated-plugin/go.mod"), `module ${plugin}\n\ngo 1.23.0\n`);
  await file(join(fixture, "go.work"), "go 1.23.0\nuse (\n ./node\n ./unrelated-plugin\n)\n");
  download();
  assert.equal(check().gowork, "off", "workspace Plugin must not contaminate isolated check");

  for (const path of [plugin, `${plugin}/nested`, cli]) {
    await manifest(`require ${path} v1.0.0`);
    download();
    assert.throws(() => check(), /forbidden module/, "unused require must still fail");
  }
  await manifest();
  download();
  check();
  await manifest(`require ${plugin} v1.0.0`);
  assert.throws(() => check(), /forbidden module/, "success/cache must not mask restored violation");

  await manifest(`require ${middle} v1.0.0`);
  download();
  assert.throws(() => check(), /forbidden module/, "indirect edge must fail without a Plugin import");
  await manifest(`replace ${leaf} v1.0.0 => ../unrelated-plugin`);
  assert.throws(() => check(), /replacements/);

  await manifest();
  await file(join(root, "internal/host/host.go"), "package host\nconst Value = 1\n");
  await source(`import _ "${nodeModule}/internal/host"`);
  assert.throws(() => check(), /forbidden package/);
  await manifest(`require ${cobra} v1.0.0`);
  await source(`import _ "${cobra}"`);
  download();
  assert.throws(() => check(), /forbidden package/);

  await manifest();
  await source();
  await file(join(root, "pkg/adapters/platform_windows.go"), `package adapters\nimport _ "${nodeModule}/internal/host"\n`);
  check(["linux/amd64"]);
  assert.throws(() => check(["windows/amd64"]), /forbidden package/);
  await rm(join(root, "pkg/adapters/platform_windows.go"));
  await file(join(root, "pkg/adapters/platform_cgo.go"), `//go:build cgo\n\npackage adapters\nimport _ "${nodeModule}/internal/host"\n`);
  assert.throws(() => check(), /forbidden package/, "cgo consumers must not escape the library boundary");
  await rm(join(root, "pkg/adapters/platform_cgo.go"));
  await rm(join(root, "pkg/adapters/value.go"));
  assert.throws(() => check(), /no adapter packages|empty Go JSON/);
  await rmdir(join(root, "pkg/adapters"));
  assert.throws(() => check(), /ENOENT/);
});
