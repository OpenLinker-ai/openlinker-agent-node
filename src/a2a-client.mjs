import { AgentNodeError } from "./errors.mjs";
import { joinAPIPath, readJSONResponse } from "./util.mjs";

export class AgentA2AClient {
  constructor({ apiBase, runtimeToken, fetchImpl = globalThis.fetch } = {}) {
    if (!apiBase) throw new AgentNodeError("apiBase is required", { code: "CONFIG_ERROR" });
    if (!runtimeToken) throw new AgentNodeError("runtimeToken is required", { code: "CONFIG_ERROR" });
    if (!fetchImpl) throw new AgentNodeError("fetch is unavailable", { code: "CONFIG_ERROR" });
    this.apiBase = apiBase;
    this.runtimeToken = runtimeToken;
    this.fetch = fetchImpl;
  }

  async callAgent({
    currentRunId,
    targetAgentId,
    input,
    reason = "",
    metadata,
    endpoint = "/api/v1/agent-runtime/call-agent",
  }) {
    if (!currentRunId) throw new AgentNodeError("currentRunId is required", { code: "A2A_CURRENT_RUN_REQUIRED" });
    if (!targetAgentId) throw new AgentNodeError("targetAgentId is required", { code: "A2A_TARGET_REQUIRED" });
    if (input === undefined) throw new AgentNodeError("input is required", { code: "A2A_INPUT_REQUIRED" });

    const res = await this.fetch(joinAPIPath(this.apiBase, endpoint), {
      method: "POST",
      headers: {
        authorization: `Bearer ${this.runtimeToken}`,
        "content-type": "application/json",
      },
      body: JSON.stringify({
        current_run_id: currentRunId,
        target_agent_id: targetAgentId,
        reason,
        input,
        metadata,
      }),
    });
    const json = await readJSONResponse(res);
    if (!res.ok) {
      throw new AgentNodeError(`A2A call failed with HTTP ${res.status}`, {
        code: json?.error?.code ?? "A2A_CALL_FAILED",
        status: res.status,
        data: json,
      });
    }
    return json;
  }
}

export class PublicA2AClient {
  constructor({ apiBase, token, fetchImpl = globalThis.fetch } = {}) {
    if (!apiBase) throw new AgentNodeError("apiBase is required", { code: "CONFIG_ERROR" });
    if (!token) throw new AgentNodeError("token is required", { code: "CONFIG_ERROR" });
    this.apiBase = apiBase;
    this.token = token;
    this.fetch = fetchImpl;
  }

  async sendMessage({
    slug,
    text,
    parts,
    messageId = `msg-${Date.now()}`,
    contextId,
    blocking = true,
    metadata,
    acceptedOutputModes = ["application/json", "text/plain"],
  }) {
    if (!slug) throw new AgentNodeError("slug is required", { code: "A2A_SLUG_REQUIRED" });
    const finalParts = parts ?? [{ kind: "text", text: text ?? "" }];
    const res = await this.fetch(joinAPIPath(this.apiBase, `/api/v1/a2a/agents/${encodeURIComponent(slug)}`), {
      method: "POST",
      headers: {
        authorization: `Bearer ${this.token}`,
        "content-type": "application/json",
        "a2a-version": "1.0",
      },
      body: JSON.stringify({
        jsonrpc: "2.0",
        id: messageId,
        method: "SendMessage",
        params: {
          message: {
            messageId,
            contextId,
            role: "user",
            parts: finalParts,
          },
          configuration: { blocking, acceptedOutputModes },
          metadata,
        },
      }),
    });
    const json = await readJSONResponse(res);
    if (!res.ok || json?.error) {
      throw new AgentNodeError(`A2A SendMessage failed with HTTP ${res.status}`, {
        code: json?.error?.code ?? "A2A_SEND_FAILED",
        status: res.status,
        data: json,
      });
    }
    return json.result;
  }

  async getTask({ slug, taskId, historyLength } = {}) {
    if (!slug) throw new AgentNodeError("slug is required", { code: "A2A_SLUG_REQUIRED" });
    if (!taskId) throw new AgentNodeError("taskId is required", { code: "A2A_TASK_REQUIRED" });
    const query = historyLength === undefined ? "" : `?historyLength=${encodeURIComponent(historyLength)}`;
    const res = await this.fetch(joinAPIPath(this.apiBase, `/api/v1/a2a/agents/${encodeURIComponent(slug)}/tasks/${encodeURIComponent(taskId)}${query}`), {
      headers: {
        authorization: `Bearer ${this.token}`,
        "a2a-version": "1.0",
      },
    });
    const json = await readJSONResponse(res);
    if (!res.ok) {
      throw new AgentNodeError(`A2A GetTask failed with HTTP ${res.status}`, {
        code: json?.error?.code ?? "A2A_TASK_FAILED",
        status: res.status,
        data: json,
      });
    }
    return json;
  }
}
