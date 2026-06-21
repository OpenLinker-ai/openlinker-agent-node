import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { AgentA2AClient, PublicA2AClient } from "../src/a2a-client.mjs";
import { close, createJSONServer, listen } from "./helpers.mjs";

describe("AgentA2AClient", () => {
  it("calls the runtime call-agent endpoint with current run context", async () => {
    let received = null;
    const server = createJSONServer((req, body) => {
      received = { req, body };
      return { body: { run_id: "child-run-1", status: "running" } };
    });
    const address = await listen(server);
    const apiBase = `http://127.0.0.1:${address.port}`;
    try {
      const client = new AgentA2AClient({ apiBase, runtimeToken: "ol_live_runtime" });
      const result = await client.callAgent({
        currentRunId: "parent-run-1",
        targetAgentId: "target-agent-1",
        input: { q: "search" },
        reason: "delegate",
      });

      assert.equal(result.run_id, "child-run-1");
      assert.equal(received.req.method, "POST");
      assert.equal(received.req.url, "/api/v1/agent-runtime/call-agent");
      assert.equal(received.req.headers.authorization, "Bearer ol_live_runtime");
      assert.deepEqual(received.body, {
        current_run_id: "parent-run-1",
        target_agent_id: "target-agent-1",
        reason: "delegate",
        input: { q: "search" },
      });
    } finally {
      await close(server);
    }
  });
});

describe("PublicA2AClient", () => {
  it("sends JSON-RPC SendMessage with A2A-Version 1.0", async () => {
    let received = null;
    const server = createJSONServer((req, body) => {
      received = { req, body };
      return { body: { jsonrpc: "2.0", id: body.id, result: { id: "task-1", status: { state: "completed" } } } };
    });
    const address = await listen(server);
    const apiBase = `http://127.0.0.1:${address.port}`;
    try {
      const client = new PublicA2AClient({ apiBase, token: "user-token" });
      const result = await client.sendMessage({ slug: "demo-agent", text: "hello", messageId: "msg-1" });

      assert.equal(result.id, "task-1");
      assert.equal(received.req.url, "/api/v1/a2a/agents/demo-agent");
      assert.equal(received.req.headers.authorization, "Bearer user-token");
      assert.equal(received.req.headers["a2a-version"], "1.0");
      assert.equal(received.body.method, "SendMessage");
      assert.equal(received.body.params.message.parts[0].text, "hello");
    } finally {
      await close(server);
    }
  });
});
