import assert from "node:assert/strict";
import http from "node:http";
import { describe, it } from "node:test";
import { WebSocketServer } from "ws";
import { AgentNode } from "../src/node.mjs";
import { RuntimeWSConnector } from "../src/runtime-ws-connector.mjs";
import { createFunctionAdapter } from "../src/adapters/index.mjs";
import { close, listen, waitFor } from "./helpers.mjs";

describe("AgentNode runtime_ws", () => {
  it("receives assignments, emits progress, delegates through A2A, and sends a result", async () => {
    const wsMessages = [];
    let callAgentBody = null;
    const server = http.createServer((req, res) => {
      if (req.method === "POST" && req.url === "/api/v1/agent-runtime/call-agent") {
        let raw = "";
        req.on("data", (chunk) => { raw += chunk; });
        req.on("end", () => {
          callAgentBody = JSON.parse(raw);
          assert.equal(req.headers.authorization, "Bearer ol_live_ws");
          res.writeHead(200, { "content-type": "application/json" });
          res.end(JSON.stringify({ run_id: "child-run-ws", status: "success", output: { answer: "child" } }));
        });
        return;
      }
      res.writeHead(404);
      res.end();
    });
    const wss = new WebSocketServer({ noServer: true });
    server.on("upgrade", (req, socket, head) => {
      assert.equal(req.url, "/api/v1/agent-runtime/ws");
      assert.equal(req.headers.authorization, "Bearer ol_live_ws");
      wss.handleUpgrade(req, socket, head, (ws) => wss.emit("connection", ws, req));
    });
    wss.on("connection", (ws) => {
      ws.send(JSON.stringify({ type: "runtime.ready", agent_id: "agent-ws" }));
      ws.send(JSON.stringify({
        type: "run.assigned",
        run_id: "run-ws",
        agent_id: "agent-ws",
        input: { task: "delegate" },
        metadata: { source: "test" },
        source: "test",
        a2a: {
          current_run_id: "run-ws",
          call_agent_endpoint: "/api/v1/agent-runtime/call-agent",
        },
      }));
      ws.on("message", (data) => {
        wsMessages.push(JSON.parse(data.toString("utf8")));
      });
    });
    const address = await listen(server);
    const apiBase = `http://127.0.0.1:${address.port}`;
    const adapter = createFunctionAdapter(async (input, ctx) => {
      ctx.emit("run.message.delta", { text: `handling ${input.task}` });
      const child = await ctx.callAgent("target-agent", { q: input.task }, { reason: "need child" });
      return {
        output: {
          handled: true,
          child_run_id: child.run_id,
        },
      };
    });
    const connector = new RuntimeWSConnector({ apiBase, runtimeToken: "ol_live_ws", reconnect: false });
    const node = new AgentNode({ apiBase, runtimeToken: "ol_live_ws", connector, adapter, logger: silentLogger() });

    try {
      await node.start();
      await waitFor(() => wsMessages.find((msg) => msg.type === "run.result"));
      const event = wsMessages.find((msg) => msg.type === "run.event");
      const result = wsMessages.find((msg) => msg.type === "run.result");

      assert.equal(event.run_id, "run-ws");
      assert.equal(event.event_type, "run.message.delta");
      assert.deepEqual(event.payload, { text: "handling delegate" });
      assert.equal(result.run_id, "run-ws");
      assert.equal(result.status, "success");
      assert.deepEqual(result.output, { handled: true, child_run_id: "child-run-ws" });
      assert.deepEqual(callAgentBody, {
        current_run_id: "run-ws",
        target_agent_id: "target-agent",
        reason: "need child",
        input: { q: "delegate" },
      });
    } finally {
      await node.stop();
      wss.close();
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
