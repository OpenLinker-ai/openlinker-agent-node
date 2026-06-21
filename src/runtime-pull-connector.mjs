import { AgentNodeError } from "./errors.mjs";
import { joinAPIPath, readJSONResponse, retryAfterMs, sleep } from "./util.mjs";

export class RuntimePullConnector {
  supportsLiveEvents = false;

  constructor({
    apiBase,
    runtimeToken,
    fetchImpl = globalThis.fetch,
    waitSeconds = 25,
    heartbeatSeconds = 60,
    emptyRetrySeconds = 5,
    maxRuns = 0,
    stopOnEmpty = false,
    logger = console,
  } = {}) {
    if (!apiBase) throw new AgentNodeError("apiBase is required", { code: "CONFIG_ERROR" });
    if (!runtimeToken) throw new AgentNodeError("runtimeToken is required", { code: "CONFIG_ERROR" });
    this.apiBase = apiBase;
    this.runtimeToken = runtimeToken;
    this.fetch = fetchImpl;
    this.waitSeconds = waitSeconds;
    this.heartbeatSeconds = heartbeatSeconds;
    this.emptyRetrySeconds = emptyRetrySeconds;
    this.maxRuns = maxRuns;
    this.stopOnEmpty = stopOnEmpty;
    this.logger = logger;
    this.shouldRun = false;
    this.handlers = {};
    this.processed = 0;
    this.abort = new AbortController();
  }

  async start(handlers = {}) {
    this.handlers = handlers;
    this.shouldRun = true;
    this.abort = new AbortController();
    void this.loop();
  }

  async stop() {
    this.shouldRun = false;
    this.abort.abort();
  }

  async loop() {
    let lastHeartbeat = 0;
    while (this.shouldRun && (this.maxRuns === 0 || this.processed < this.maxRuns)) {
      const now = Date.now();
      if (now - lastHeartbeat >= this.heartbeatSeconds * 1000) {
        await this.heartbeat();
        lastHeartbeat = Date.now();
      }
      const claim = await this.claimOnce();
      if (claim.status === 204) {
        if (this.stopOnEmpty) break;
        await sleep(retryAfterMs(claim.res, this.emptyRetrySeconds), this.abort.signal);
        continue;
      }
      if (claim.status === 429) {
        await sleep(retryAfterMs(claim.res, this.emptyRetrySeconds), this.abort.signal);
        continue;
      }
      if (claim.status !== 200) {
        this.handlers.onError?.(new AgentNodeError(`claim returned ${claim.status}`, { code: "RUNTIME_PULL_CLAIM_FAILED", data: claim.json }));
        await sleep(this.emptyRetrySeconds * 1000, this.abort.signal);
        continue;
      }
      await this.handlers.onAssigned?.(normalizeClaim(claim.json));
      this.processed += 1;
    }
  }

  async heartbeat() {
    return this.request("POST", "/api/v1/agent-runtime/heartbeat", undefined, [200, 429]);
  }

  async claimOnce() {
    return this.request("GET", `/api/v1/agent-runtime/runs/claim?wait=${encodeURIComponent(this.waitSeconds)}`, undefined, [200, 204, 429]);
  }

  async completeRun(runId, result) {
    const response = await this.request("POST", `/api/v1/agent-runtime/runs/${encodeURIComponent(runId)}/result`, {
      status: result.status,
      output: result.output,
      events: result.events,
      error: result.error,
      duration_ms: Math.round(result.duration_ms ?? 0),
    }, [200]);
    return response.json;
  }

  async sendRunEvent() {
    return undefined;
  }

  async request(method, pathName, body, expected) {
    const res = await this.fetch(joinAPIPath(this.apiBase, pathName), {
      method,
      headers: {
        authorization: `Bearer ${this.runtimeToken}`,
        ...(body === undefined ? {} : { "content-type": "application/json" }),
      },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: this.abort.signal,
    });
    const json = await readJSONResponse(res);
    if (!expected.includes(res.status)) {
      throw new AgentNodeError(`${method} ${pathName} returned ${res.status}`, {
        code: "RUNTIME_PULL_HTTP_ERROR",
        status: res.status,
        data: json,
      });
    }
    return { res, json, status: res.status };
  }
}

function normalizeClaim(claim) {
  return {
    type: "run.assigned",
    run_id: claim.run_id,
    agent_id: claim.agent_id,
    input: claim.input,
    metadata: claim.metadata,
    source: claim.source,
    result_endpoint: claim.result_endpoint,
    result_method: claim.result_method,
    result_required: claim.result_required,
    a2a: claim.a2a,
  };
}
