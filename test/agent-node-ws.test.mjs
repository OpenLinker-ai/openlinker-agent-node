import assert from "node:assert/strict";
import http from "node:http";
import { describe, it } from "node:test";
import { WebSocketServer } from "ws";
import { LocalHelperServer } from "../src/local-helper-server.mjs";
import { AgentNode } from "../src/node.mjs";
import { RuntimeWSConnector } from "../src/runtime-ws-connector.mjs";
import { createFunctionAdapter, createHTTPAdapter } from "../src/adapters/index.mjs";
import { close, createJSONServer, listen, waitFor } from "./helpers.mjs";

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

  it("lets HTTP backends call A2A and emit events through the localhost helper", async () => {
    const wsMessages = [];
    let callAgentBody = null;
    let helperInfo = null;
    const platform = http.createServer((req, res) => {
      if (req.method === "POST" && req.url === "/api/v1/agent-runtime/call-agent") {
        let raw = "";
        req.on("data", (chunk) => { raw += chunk; });
        req.on("end", () => {
          callAgentBody = JSON.parse(raw);
          assert.equal(req.headers.authorization, "Bearer ol_live_helper");
          res.writeHead(200, { "content-type": "application/json" });
          res.end(JSON.stringify({ run_id: "child-run-helper", status: "success", output: { answer: "helper child" } }));
        });
        return;
      }
      res.writeHead(404);
      res.end();
    });
    const wss = new WebSocketServer({ noServer: true });
    platform.on("upgrade", (req, socket, head) => {
      assert.equal(req.url, "/api/v1/agent-runtime/ws");
      assert.equal(req.headers.authorization, "Bearer ol_live_helper");
      wss.handleUpgrade(req, socket, head, (ws) => wss.emit("connection", ws, req));
    });
    wss.on("connection", (ws) => {
      ws.send(JSON.stringify({ type: "runtime.ready", agent_id: "agent-helper" }));
      ws.send(JSON.stringify({
        type: "run.assigned",
        run_id: "run-helper",
        agent_id: "agent-helper",
        input: { task: "openclaw" },
        metadata: { source: "helper-test" },
        a2a: {
          current_run_id: "run-helper",
          call_agent_endpoint: "/api/v1/agent-runtime/call-agent",
        },
      }));
      ws.on("message", (data) => {
        wsMessages.push(JSON.parse(data.toString("utf8")));
      });
    });

    const backend = createJSONServer(async (req, body) => {
      assert.equal(req.url, "/run");
      helperInfo = body.agent_node.helper;
      assert.match(helperInfo.base_url, /^http:\/\/127\.0\.0\.1:\d+$/);
      assert.match(helperInfo.token, /^olh_/);
      const eventResponse = await fetch(helperInfo.endpoints.events, {
        method: "POST",
        headers: {
          "content-type": "application/json",
          ...helperInfo.headers,
        },
        body: JSON.stringify({
          run_id: body.run_id,
          event_type: "run.message.delta",
          payload: { text: `backend handling ${body.input.task}` },
        }),
      });
      assert.equal(eventResponse.status, 200);
      const child = await fetch(helperInfo.endpoints.call_agent, {
        method: "POST",
        headers: {
          "content-type": "application/json",
          ...helperInfo.headers,
        },
        body: JSON.stringify({
          target_agent_id: "target-helper",
          reason: "backend delegation",
          input: { q: body.input.task },
        }),
      });
      assert.equal(child.status, 200);
      const childJSON = await child.json();
      return {
        body: {
          output: {
            handled_by: "openclaw-http",
            child_run_id: childJSON.run_id,
          },
        },
      };
    });

    const platformAddress = await listen(platform);
    const backendAddress = await listen(backend);
    const apiBase = `http://127.0.0.1:${platformAddress.port}`;
    const adapter = createHTTPAdapter({ url: `http://127.0.0.1:${backendAddress.port}/run` });
    const connector = new RuntimeWSConnector({ apiBase, runtimeToken: "ol_live_helper", reconnect: false });
    const helper = new LocalHelperServer({ logger: silentLogger() });
    const node = new AgentNode({ apiBase, runtimeToken: "ol_live_helper", connector, adapter, helper, logger: silentLogger() });

    try {
      await node.start();
      await waitFor(() => wsMessages.find((msg) => msg.type === "run.result"));
      const event = wsMessages.find((msg) => msg.type === "run.event");
      const result = wsMessages.find((msg) => msg.type === "run.result");

      assert.deepEqual(callAgentBody, {
        current_run_id: "run-helper",
        target_agent_id: "target-helper",
        reason: "backend delegation",
        input: { q: "openclaw" },
      });
      assert.equal(event.run_id, "run-helper");
      assert.deepEqual(event.payload, { text: "backend handling openclaw" });
      assert.deepEqual(result.output, {
        handled_by: "openclaw-http",
        child_run_id: "child-run-helper",
      });

      const expired = await fetch(helperInfo.endpoints.events, {
        method: "POST",
        headers: {
          "content-type": "application/json",
          ...helperInfo.headers,
        },
        body: JSON.stringify({ event_type: "run.message.delta", payload: { text: "late" } }),
      });
      assert.equal(expired.status, 401);
    } finally {
      await node.stop();
      wss.close();
      await close(backend);
      await close(platform);
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
