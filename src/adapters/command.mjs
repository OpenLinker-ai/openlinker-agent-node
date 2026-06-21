import { spawn } from "node:child_process";
import { AgentNodeError } from "../errors.mjs";

export function createCommandAdapter({
  command,
  args = [],
  cwd = process.cwd(),
  env = process.env,
  timeoutMs = 15 * 60_000,
} = {}) {
  if (!command) throw new AgentNodeError("command is required", { code: "ADAPTER_CONFIG_ERROR" });
  return {
    async run(input, ctx) {
      const payload = JSON.stringify({
        input,
        run_id: ctx.runId,
        metadata: ctx.metadata,
        a2a: ctx.a2a,
      });
      const startedAt = Date.now();
      const child = spawn(command, args, {
        cwd,
        env: sanitizedEnv(env),
        stdio: ["pipe", "pipe", "pipe"],
      });
      let stdout = "";
      let stderr = "";
      child.stdout.on("data", (chunk) => {
        stdout += chunk;
      });
      child.stderr.on("data", (chunk) => {
        stderr += chunk;
      });
      child.stdin.end(payload);

      const exitCode = await new Promise((resolve, reject) => {
        const timer = setTimeout(() => {
          child.kill("SIGTERM");
          reject(new AgentNodeError(`command timed out after ${timeoutMs}ms`, { code: "COMMAND_TIMEOUT" }));
        }, timeoutMs);
        child.once("error", (error) => {
          clearTimeout(timer);
          reject(error);
        });
        child.once("exit", (code) => {
          clearTimeout(timer);
          resolve(code);
        });
      });
      if (exitCode !== 0) {
        throw new AgentNodeError(`command exited ${exitCode}: ${stderr || stdout}`, { code: "COMMAND_FAILED" });
      }
      return parseCommandOutput(stdout, stderr, Date.now() - startedAt);
    },
  };
}

function parseCommandOutput(stdout, stderr, durationMs) {
  const text = stdout.trim();
  if (!text) {
    return { ok: true, duration_ms: durationMs, stderr: stderr.trim() };
  }
  try {
    const json = JSON.parse(text);
    return json?.output === undefined ? json : json.output;
  } catch {
    return {
      text,
      stderr: stderr.trim() || undefined,
      duration_ms: durationMs,
    };
  }
}

export function sanitizedEnv(env) {
  const next = { ...env };
  for (const key of Object.keys(next)) {
    if (key.startsWith("OPENLINKER_") && /(TOKEN|JWT|PASSWORD|SECRET|KEY)/i.test(key)) {
      delete next[key];
    }
  }
  return next;
}
