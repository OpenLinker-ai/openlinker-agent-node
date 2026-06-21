import WebSocket from "ws";
import { AgentNodeError } from "./errors.mjs";
import { sleep, websocketURL } from "./util.mjs";

export class RuntimeWSConnector {
  supportsLiveEvents = true;

  constructor({
    apiBase,
    runtimeToken,
    WebSocketImpl = WebSocket,
    reconnect = true,
    reconnectBaseMs = 500,
    reconnectMaxMs = 10_000,
    logger = console,
  } = {}) {
    if (!apiBase) throw new AgentNodeError("apiBase is required", { code: "CONFIG_ERROR" });
    if (!runtimeToken) throw new AgentNodeError("runtimeToken is required", { code: "CONFIG_ERROR" });
    this.apiBase = apiBase;
    this.runtimeToken = runtimeToken;
    this.WebSocketImpl = WebSocketImpl;
    this.reconnect = reconnect;
    this.reconnectBaseMs = reconnectBaseMs;
    this.reconnectMaxMs = reconnectMaxMs;
    this.logger = logger;
    this.shouldRun = false;
    this.ws = null;
    this.handlers = {};
    this.connecting = null;
  }

  async start(handlers = {}) {
    this.handlers = handlers;
    this.shouldRun = true;
    await this.connect();
  }

  async stop() {
    this.shouldRun = false;
    if (this.ws && this.ws.readyState === this.WebSocketImpl.OPEN) {
      this.ws.close();
    }
  }

  async connect() {
    if (this.connecting) return this.connecting;
    this.connecting = new Promise((resolve, reject) => {
      const ws = new this.WebSocketImpl(websocketURL(this.apiBase, "/api/v1/agent-runtime/ws"), {
        headers: { authorization: `Bearer ${this.runtimeToken}` },
      });
      this.ws = ws;
      let opened = false;
      ws.once("open", () => {
        opened = true;
        resolve();
      });
      ws.once("error", (error) => {
        this.handlers.onError?.(error);
        if (!opened) reject(error);
      });
      ws.on("message", (data) => {
        void this.handleMessage(data);
      });
      ws.once("close", () => {
        this.ws = null;
        if (this.shouldRun && this.reconnect) {
          void this.reconnectLoop();
        }
      });
    }).finally(() => {
      this.connecting = null;
    });
    return this.connecting;
  }

  async reconnectLoop() {
    let delay = this.reconnectBaseMs;
    while (this.shouldRun) {
      await sleep(delay);
      try {
        await this.connect();
        return;
      } catch (error) {
        this.handlers.onError?.(error);
        delay = Math.min(this.reconnectMaxMs, delay * 2);
      }
    }
  }

  async handleMessage(data) {
    let message;
    try {
      message = JSON.parse(Buffer.isBuffer(data) ? data.toString("utf8") : String(data));
    } catch (error) {
      this.handlers.onError?.(error);
      return;
    }
    switch (message.type) {
      case "runtime.ready":
        this.handlers.onReady?.(message);
        break;
      case "run.assigned":
        await this.handlers.onAssigned?.(message);
        break;
      case "error":
        this.handlers.onError?.(message.error ?? message);
        break;
      default:
        this.handlers.onMessage?.(message);
    }
  }

  async heartbeat(id = `hb-${Date.now()}`) {
    this.send({ type: "heartbeat", id });
  }

  async claim(id = `claim-${Date.now()}`) {
    this.send({ type: "runtime.claim", id });
  }

  async sendRunEvent(runId, eventType, payload = {}) {
    this.send({
      type: "run.event",
      id: `event-${runId}-${Date.now()}`,
      run_id: runId,
      event_type: eventType,
      payload,
    });
  }

  async completeRun(runId, result) {
    this.send({
      type: "run.result",
      id: `result-${runId}-${Date.now()}`,
      run_id: runId,
      status: result.status,
      output: result.output,
      events: result.events,
      error: result.error,
      duration_ms: Math.round(result.duration_ms ?? 0),
    });
  }

  send(message) {
    if (!this.ws || this.ws.readyState !== this.WebSocketImpl.OPEN) {
      throw new AgentNodeError("runtime websocket is not open", { code: "RUNTIME_WS_CLOSED" });
    }
    this.ws.send(JSON.stringify(message));
  }
}
