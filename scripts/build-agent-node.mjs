import { spawnSync } from "node:child_process";
import { resolve, dirname } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const repository = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const versionSymbol = "github.com/OpenLinker-ai/openlinker-agent-node/internal/agentnode.AgentNodeVersion";

export function buildAgentNode(version, output, { env = process.env } = {}) {
  if (typeof version !== "string" || version !== version.trim() || !/^(?:dev|sha-[0-9a-f]{12,40}|v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?(?:\+[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?)$/.test(version)) {
    throw new Error("version must be dev, an exact v-prefixed release version, or sha-<12..40 lowercase hex>");
  }
  if (typeof output !== "string" || output.length === 0) throw new Error("output path is required");
  const result = spawnSync("go", [
    "build", "-trimpath", "-mod=readonly",
    `-ldflags=-s -w -X ${versionSymbol}=openlinker-agent-node/${version}`,
    "-o", resolve(output), "./cmd/openlinker-agent-node",
  ], {
    cwd: repository,
    env: { ...env, GOWORK: "off", CGO_ENABLED: "0" },
    encoding: "utf8", timeout: 120_000, maxBuffer: 1024 * 1024,
  });
  if (result.error || result.status !== 0) {
    // Compiler diagnostics may include environment-dependent paths; the command
    // reports a bounded classification instead of echoing an entire subprocess.
    throw new Error(`Agent Node build failed (${result.error?.code ?? result.status ?? "terminated"})`);
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try {
    if (process.argv.length !== 4) throw new Error("usage: node scripts/build-agent-node.mjs <version> <output>");
    buildAgentNode(process.argv[2], process.argv[3]);
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  }
}
