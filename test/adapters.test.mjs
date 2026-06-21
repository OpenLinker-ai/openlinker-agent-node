import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { createCodexAdapter, createCommandAdapter, createFunctionAdapter, createHTTPAdapter } from "../src/adapters/index.mjs";
import { buildCodexPrompt } from "../src/adapters/codex.mjs";
import { close, createJSONServer, listen } from "./helpers.mjs";

describe("adapters", () => {
  it("wraps a function backend", async () => {
    const adapter = createFunctionAdapter(async (input, ctx) => ({ answer: `${ctx.runId}:${input.q}` }));
    const output = await adapter.run({ q: "ok" }, { runId: "run-1" });
    assert.deepEqual(output, { answer: "run-1:ok" });
  });

  it("posts the OpenLinker envelope to an HTTP backend", async () => {
    let received = null;
    const server = createJSONServer((req, body) => {
      received = { req, body };
      return { body: { output: { ok: true, seen: body.input.q, run_id: body.run_id } } };
    });
    const address = await listen(server);
    try {
      const adapter = createHTTPAdapter({ url: `http://127.0.0.1:${address.port}/run` });
      const output = await adapter.run({ q: "http" }, {
        runId: "run-http",
        metadata: { source: "test" },
        a2a: { current_run_id: "run-http" },
      });

      assert.equal(received.req.url, "/run");
      assert.deepEqual(received.body.input, { q: "http" });
      assert.equal(received.body.run_id, "run-http");
      assert.deepEqual(output, { ok: true, seen: "http", run_id: "run-http" });
    } finally {
      await close(server);
    }
  });

  it("runs a command backend with JSON stdin and JSON stdout", async () => {
    const code = `
      let raw = "";
      process.stdin.on("data", (chunk) => raw += chunk);
      process.stdin.on("end", () => {
        const body = JSON.parse(raw);
        process.stdout.write(JSON.stringify({ output: {
          ok: true,
          q: body.input.q,
          run_id: body.run_id,
          helper_url: process.env.OPENLINKER_AGENT_NODE_HELPER_URL,
          helper_token: process.env.OPENLINKER_AGENT_NODE_HELPER_TOKEN,
          call_agent_url: process.env.OPENLINKER_AGENT_NODE_HELPER_CALL_AGENT_URL,
          envelope_helper_token: body.agent_node.helper.token
        } }));
      });
    `;
    const adapter = createCommandAdapter({
      command: process.execPath,
      args: ["-e", code],
      timeoutMs: 3000,
    });
    const output = await adapter.run({ q: "cli" }, {
      runId: "run-cli",
      metadata: {},
      a2a: {},
      helper: helperContext(),
    });
    assert.deepEqual(output, {
      ok: true,
      q: "cli",
      run_id: "run-cli",
      helper_url: "http://127.0.0.1:19090",
      helper_token: "olh_test",
      call_agent_url: "http://127.0.0.1:19090/a2a/call",
      envelope_helper_token: "olh_test",
    });
  });

  it("supports Codex mock mode for deterministic Agent Node tests", async () => {
    const events = [];
    const adapter = createCodexAdapter({ mockResponse: "mocked codex result" });
    const output = await adapter.run({ task: "explain" }, {
      runId: "run-codex",
      metadata: {},
      a2a: {},
      emit(eventType, payload) {
        events.push({ eventType, payload });
      },
    });
    assert.deepEqual(events, [{ eventType: "run.message.delta", payload: { text: "Codex adapter started." } }]);
    assert.deepEqual(output, {
      handled_by: "codex",
      mocked: true,
      summary: "mocked codex result",
    });
  });

  it("includes localhost helper instructions in the Codex prompt", () => {
    const prompt = buildCodexPrompt({ task: "delegate" }, {
      runId: "run-codex-helper",
      metadata: {},
      a2a: { current_run_id: "run-codex-helper" },
      helper: helperContext(),
    });

    assert.match(prompt, /agent_node\.helper\.endpoints\.call_agent/);
    assert.match(prompt, /agent_node\.helper\.headers\.authorization/);
    assert.match(prompt, /http:\/\/127\.0\.0\.1:19090\/a2a\/call/);
    assert.match(prompt, /olh_test/);
  });
});

function helperContext() {
  return {
    base_url: "http://127.0.0.1:19090",
    token: "olh_test",
    headers: {
      authorization: "Bearer olh_test",
    },
    endpoints: {
      call_agent: "http://127.0.0.1:19090/a2a/call",
      events: "http://127.0.0.1:19090/events",
    },
  };
}
