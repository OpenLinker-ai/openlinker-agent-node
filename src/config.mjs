import { AgentNode } from "./node.mjs";
import { LocalHelperServer } from "./local-helper-server.mjs";
import { RuntimePullConnector } from "./runtime-pull-connector.mjs";
import { RuntimeWSConnector } from "./runtime-ws-connector.mjs";
import {
  createCodexAdapter,
  createCommandAdapter,
  createHTTPAdapter,
  createModuleAdapter,
} from "./adapters/index.mjs";
import { boolOption, numberOption, parseJSONOption } from "./util.mjs";

export async function createAgentNodeFromEnv(env = process.env) {
  const apiBase = env.OPENLINKER_API_BASE ?? env.OPENLINKER_API_ROOT?.replace(/\/api\/v1\/?$/, "");
  const runtimeToken = env.OPENLINKER_RUNTIME_TOKEN;
  const adapterMode = env.OPENLINKER_AGENT_NODE_ADAPTER ?? "module";
  const connector = createConnectorFromEnv(env, { apiBase, runtimeToken });
  const adapter = await createAdapterFromEnv(env);
  const helper = createLocalHelperFromEnv(env, { adapterMode });
  return new AgentNode({
    apiBase,
    runtimeToken,
    connector,
    adapter,
    helper,
  });
}

export function createConnectorFromEnv(env, { apiBase, runtimeToken }) {
  const mode = env.OPENLINKER_AGENT_NODE_CONNECTOR ?? "runtime_ws";
  if (mode === "runtime_pull") {
    return new RuntimePullConnector({
      apiBase,
      runtimeToken,
      waitSeconds: numberOption(env.OPENLINKER_AGENT_NODE_PULL_WAIT_SECONDS, 25, "OPENLINKER_AGENT_NODE_PULL_WAIT_SECONDS"),
      heartbeatSeconds: numberOption(env.OPENLINKER_AGENT_NODE_HEARTBEAT_SECONDS, 60, "OPENLINKER_AGENT_NODE_HEARTBEAT_SECONDS"),
      maxRuns: numberOption(env.OPENLINKER_AGENT_NODE_MAX_RUNS, 0, "OPENLINKER_AGENT_NODE_MAX_RUNS"),
    });
  }
  if (mode !== "runtime_ws") {
    throw new Error(`unsupported OPENLINKER_AGENT_NODE_CONNECTOR=${mode}`);
  }
  return new RuntimeWSConnector({
    apiBase,
    runtimeToken,
    reconnect: boolOption(env.OPENLINKER_AGENT_NODE_RECONNECT, true),
  });
}

export async function createAdapterFromEnv(env = process.env) {
  const adapter = env.OPENLINKER_AGENT_NODE_ADAPTER ?? "module";
  switch (adapter) {
    case "module":
      return createModuleAdapter({
        modulePath: env.OPENLINKER_AGENT_NODE_MODULE,
        exportName: env.OPENLINKER_AGENT_NODE_EXPORT ?? "handle",
      });
    case "http":
      return createHTTPAdapter({
        url: env.OPENLINKER_AGENT_NODE_HTTP_URL,
        headers: parseJSONOption(env.OPENLINKER_AGENT_NODE_HTTP_HEADERS, {}, "OPENLINKER_AGENT_NODE_HTTP_HEADERS"),
        timeoutMs: numberOption(env.OPENLINKER_AGENT_NODE_TIMEOUT_MS, 15 * 60_000, "OPENLINKER_AGENT_NODE_TIMEOUT_MS"),
      });
    case "command":
      return createCommandAdapter({
        command: env.OPENLINKER_AGENT_NODE_COMMAND,
        args: parseJSONOption(env.OPENLINKER_AGENT_NODE_ARGS, [], "OPENLINKER_AGENT_NODE_ARGS"),
        cwd: env.OPENLINKER_AGENT_NODE_CWD ?? process.cwd(),
        timeoutMs: numberOption(env.OPENLINKER_AGENT_NODE_TIMEOUT_MS, 15 * 60_000, "OPENLINKER_AGENT_NODE_TIMEOUT_MS"),
      });
    case "codex":
      return createCodexAdapter({
        codexBin: env.OPENLINKER_AGENT_NODE_CODEX_BIN ?? "codex",
        workspace: env.OPENLINKER_AGENT_NODE_CODEX_WORKSPACE ?? process.cwd(),
        sandbox: env.OPENLINKER_AGENT_NODE_CODEX_SANDBOX ?? "read-only",
        approval: env.OPENLINKER_AGENT_NODE_CODEX_APPROVAL ?? "never",
        model: env.OPENLINKER_AGENT_NODE_CODEX_MODEL ?? "",
        timeoutMs: numberOption(env.OPENLINKER_AGENT_NODE_TIMEOUT_MS, 30 * 60_000, "OPENLINKER_AGENT_NODE_TIMEOUT_MS"),
        mockResponse: env.OPENLINKER_AGENT_NODE_CODEX_MOCK_RESPONSE ?? "",
      });
    default:
      throw new Error(`unsupported OPENLINKER_AGENT_NODE_ADAPTER=${adapter}`);
  }
}

export function createLocalHelperFromEnv(env = process.env, { adapterMode = env.OPENLINKER_AGENT_NODE_ADAPTER ?? "module" } = {}) {
  const mode = (env.OPENLINKER_AGENT_NODE_HELPER ?? "auto").toLowerCase();
  const enabled = mode === "auto"
    ? ["http", "command", "codex"].includes(adapterMode)
    : parseHelperBoolean(mode);
  if (!enabled) return null;
  return new LocalHelperServer({
    host: env.OPENLINKER_AGENT_NODE_HELPER_HOST ?? "127.0.0.1",
    port: numberOption(env.OPENLINKER_AGENT_NODE_HELPER_PORT, 0, "OPENLINKER_AGENT_NODE_HELPER_PORT"),
  });
}

function parseHelperBoolean(value) {
  if (["1", "true", "yes", "on"].includes(value)) return true;
  if (["0", "false", "no", "off"].includes(value)) return false;
  throw new Error(`invalid OPENLINKER_AGENT_NODE_HELPER=${value}; use auto, true, or false`);
}
