import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { AgentNode } from "../src/node.mjs";
import { RuntimePullConnector } from "../src/runtime-pull-connector.mjs";
import { createFunctionAdapter } from "../src/adapters/index.mjs";
import { close, createJSONServer, listen, waitFor } from "./helpers.mjs";

describe("AgentNode runtime_pull fallback", () => {
  it("claims a run, buffers emitted events, and posts the final result", async () => {
    let claimed = false;
    let resultBody = null;
    const server = createJSONServer((req, body) => {
      if (req.method === "POST" && req.url === "/api/v1/agent-runtime/heartbeat") {
        assert.equal(req.headers.authorization, "Bearer ol_live_pull");
        return { body: { availability_status: "healthy", pending_run_count: 1, claim_now: true } };
      }
      if (req.method === "GET" && req.url === "/api/v1/agent-runtime/runs/claim?wait=1") {
        assert.equal(req.headers.authorization, "Bearer ol_live_pull");
        if (claimed) return { status: 204, headers: { "retry-after": "1" } };
        claimed = true;
        return {
          body: {
            run_id: "run-pull",
            agent_id: "agent-pull",
            input: { task: "pull" },
            metadata: { source: "test" },
            source: "test",
            result_endpoint: "/api/v1/agent-runtime/runs/run-pull/result",
            result_method: "POST",
            result_required: true,
            a2a: { current_run_id: "run-pull" },
          },
        };
      }
      if (req.method === "POST" && req.url === "/api/v1/agent-runtime/runs/run-pull/result") {
        assert.equal(req.headers.authorization, "Bearer ol_live_pull");
        resultBody = body;
        return { body: { run_id: "run-pull", status: body.status } };
      }
      return { status: 404, body: { error: "not found" } };
    });
    const address = await listen(server);
    const apiBase = `http://127.0.0.1:${address.port}`;
    const adapter = createFunctionAdapter(async (input, ctx) => {
      ctx.emit("run.message.delta", { text: `pull ${input.task}` });
      return { ok: true, mode: "pull" };
    });
    const connector = new RuntimePullConnector({
      apiBase,
      runtimeToken: "ol_live_pull",
      waitSeconds: 1,
      heartbeatSeconds: 1,
      maxRuns: 1,
      stopOnEmpty: true,
    });
    const node = new AgentNode({ apiBase, runtimeToken: "ol_live_pull", connector, adapter, logger: silentLogger() });

    try {
      await node.start();
      await waitFor(() => resultBody);
      assert.equal(resultBody.status, "success");
      assert.deepEqual(resultBody.output, { ok: true, mode: "pull" });
      assert.deepEqual(resultBody.events, [
        { event_type: "run.message.delta", payload: { text: "pull pull" } },
      ]);
    } finally {
      await node.stop();
      await close(server);
    }
  });
});

function silentLogger() {
  return {
    info() {},
    warn() {},
    error() {},
  };
}
