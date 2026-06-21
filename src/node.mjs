import { AgentA2AClient } from "./a2a-client.mjs";
import { normalizeError } from "./errors.mjs";

export class AgentNode {
  constructor({ connector, adapter, apiBase, runtimeToken, helper = null, logger = console } = {}) {
    if (!connector) throw new Error("connector is required");
    if (!adapter) throw new Error("adapter is required");
    if (!apiBase) throw new Error("apiBase is required");
    if (!runtimeToken) throw new Error("runtimeToken is required");
    this.connector = connector;
    this.adapter = adapter;
    this.apiBase = apiBase;
    this.runtimeToken = runtimeToken;
    this.helper = helper;
    this.logger = logger;
    this.queue = [];
    this.processing = false;
    this.stopped = false;
  }

  async start() {
    this.stopped = false;
    await this.helper?.start?.();
    await this.connector.start({
      onAssigned: async (assignment) => this.enqueue(assignment),
      onReady: (message) => this.logger.info?.("agent node ready", message.agent_id ?? ""),
      onError: (error) => this.logger.error?.("agent node connector error", error),
    });
  }

  async stop() {
    this.stopped = true;
    await this.connector.stop?.();
    await this.helper?.stop?.();
  }

  async enqueue(assignment) {
    this.queue.push(assignment);
    await this.drain();
  }

  async drain() {
    if (this.processing) return;
    this.processing = true;
    try {
      while (!this.stopped && this.queue.length > 0) {
        const assignment = this.queue.shift();
        await this.processAssignment(assignment);
      }
    } finally {
      this.processing = false;
    }
  }

  async processAssignment(assignment) {
    const startedAt = Date.now();
    const bufferedEvents = [];
    const ctx = this.createContext(assignment, bufferedEvents);
    const helperSession = this.helper?.createSession?.({ runId: assignment.run_id, ctx });
    if (helperSession) {
      ctx.helper = helperSession.clientInfo;
    }
    try {
      const raw = await this.adapter.run(assignment.input ?? {}, ctx);
      const normalized = normalizeAdapterResult(raw);
      await this.connector.completeRun(assignment.run_id, {
        status: normalized.status,
        output: normalized.output,
        events: this.connector.supportsLiveEvents ? normalized.events : [...bufferedEvents, ...normalized.events],
        duration_ms: Math.max(1, Date.now() - startedAt),
      });
    } catch (error) {
      const failure = normalizeError(error);
      await this.connector.completeRun(assignment.run_id, {
        status: "failed",
        error: failure,
        events: this.connector.supportsLiveEvents ? [] : bufferedEvents,
        duration_ms: Math.max(1, Date.now() - startedAt),
      });
    } finally {
      helperSession?.close?.();
    }
  }

  createContext(assignment, bufferedEvents) {
    const a2aClient = new AgentA2AClient({
      apiBase: this.apiBase,
      runtimeToken: this.runtimeToken,
    });
    return {
      runId: assignment.run_id,
      agentId: assignment.agent_id,
      input: assignment.input ?? {},
      metadata: assignment.metadata ?? {},
      source: assignment.source,
      a2a: assignment.a2a,
      emit: (eventType, payload = {}) => {
        const event = { event_type: eventType, payload };
        bufferedEvents.push(event);
        if (this.connector.supportsLiveEvents) {
          void this.connector.sendRunEvent(assignment.run_id, eventType, payload).catch((error) => {
            this.logger.warn?.("agent node run.event failed", error);
          });
        }
      },
      callAgent: (targetAgentId, input, options = {}) => a2aClient.callAgent({
        currentRunId: options.currentRunId ?? assignment.a2a?.current_run_id ?? assignment.run_id,
        targetAgentId,
        input,
        reason: options.reason ?? "",
        metadata: options.metadata,
        endpoint: options.endpoint ?? assignment.a2a?.call_agent_endpoint ?? "/api/v1/agent-runtime/call-agent",
      }),
    };
  }
}

export function normalizeAdapterResult(raw) {
  if (raw && typeof raw === "object" && ("status" in raw || "output" in raw || "events" in raw)) {
    return {
      status: raw.status ?? "success",
      output: raw.output ?? omitRuntimeFields(raw),
      events: Array.isArray(raw.events) ? raw.events : [],
    };
  }
  return {
    status: "success",
    output: raw && typeof raw === "object" ? raw : { value: raw },
    events: [],
  };
}

function omitRuntimeFields(value) {
  const copy = { ...value };
  delete copy.status;
  delete copy.output;
  delete copy.events;
  delete copy.error;
  return copy;
}
