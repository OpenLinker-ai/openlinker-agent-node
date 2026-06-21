import crypto from "node:crypto";
import http from "node:http";
import { normalizeError } from "./errors.mjs";

const MAX_BODY_BYTES = 1024 * 1024;

export class LocalHelperServer {
  constructor({ host = "127.0.0.1", port = 0, logger = console } = {}) {
    this.host = host;
    this.port = port;
    this.logger = logger;
    this.server = null;
    this.baseUrl = "";
    this.sessions = new Map();
  }

  async start() {
    if (this.server) return;
    this.server = http.createServer((req, res) => {
      void this.handle(req, res).catch((error) => {
        this.logger.warn?.("agent node helper request failed", error);
        this.sendJSON(res, 500, { error: normalizeError(error) });
      });
    });
    await new Promise((resolve, reject) => {
      this.server.once("error", reject);
      this.server.listen(this.port, this.host, () => {
        this.server.off("error", reject);
        resolve();
      });
    });
    const address = this.server.address();
    this.baseUrl = `http://${address.address}:${address.port}`;
  }

  async stop() {
    this.sessions.clear();
    if (!this.server) return;
    const server = this.server;
    this.server = null;
    this.baseUrl = "";
    await new Promise((resolve, reject) => {
      server.close((error) => (error ? reject(error) : resolve()));
    });
  }

  createSession({ runId, ctx }) {
    if (!this.server || !this.baseUrl) {
      throw new Error("agent node helper server is not started");
    }
    const token = `olh_${crypto.randomBytes(24).toString("base64url")}`;
    const session = { runId, ctx };
    this.sessions.set(token, session);
    return {
      clientInfo: this.clientInfo(token),
      close: () => {
        this.sessions.delete(token);
      },
    };
  }

  clientInfo(token) {
    return {
      base_url: this.baseUrl,
      token,
      headers: {
        authorization: `Bearer ${token}`,
      },
      endpoints: {
        call_agent: `${this.baseUrl}/a2a/call`,
        events: `${this.baseUrl}/events`,
      },
    };
  }

  async handle(req, res) {
    if (req.method !== "POST") {
      this.sendJSON(res, 405, { error: { code: "METHOD_NOT_ALLOWED", message: "method not allowed" } });
      return;
    }
    const pathname = new URL(req.url, this.baseUrl || "http://127.0.0.1").pathname;
    if (pathname !== "/a2a/call" && pathname !== "/events") {
      this.sendJSON(res, 404, { error: { code: "NOT_FOUND", message: "not found" } });
      return;
    }

    const session = this.authenticate(req);
    if (!session) {
      this.sendJSON(res, 401, { error: { code: "UNAUTHORIZED", message: "invalid helper token" } });
      return;
    }

    let body;
    try {
      body = await readJSONBody(req);
    } catch (error) {
      this.sendJSON(res, 400, { error: normalizeError(error) });
      return;
    }

    if (body?.run_id && body.run_id !== session.runId) {
      this.sendJSON(res, 409, { error: { code: "RUN_MISMATCH", message: "helper token belongs to a different run" } });
      return;
    }

    if (pathname === "/events") {
      await this.handleEvent(session, body, res);
      return;
    }
    await this.handleCallAgent(session, body, res);
  }

  authenticate(req) {
    const auth = req.headers.authorization ?? "";
    const bearer = auth.toLowerCase().startsWith("bearer ") ? auth.slice("bearer ".length).trim() : "";
    const token = bearer || req.headers["x-openlinker-agent-node-token"];
    if (!token || Array.isArray(token)) return null;
    return this.sessions.get(token) ?? null;
  }

  async handleEvent(session, body, res) {
    if (!body || typeof body.event_type !== "string" || body.event_type.length === 0) {
      this.sendJSON(res, 400, { error: { code: "INVALID_EVENT", message: "event_type is required" } });
      return;
    }
    session.ctx.emit(body.event_type, body.payload ?? {});
    this.sendJSON(res, 200, { ok: true, run_id: session.runId });
  }

  async handleCallAgent(session, body, res) {
    if (!body || typeof body.target_agent_id !== "string" || body.target_agent_id.length === 0) {
      this.sendJSON(res, 400, { error: { code: "INVALID_TARGET_AGENT", message: "target_agent_id is required" } });
      return;
    }
    try {
      const result = await session.ctx.callAgent(body.target_agent_id, body.input ?? {}, {
        reason: body.reason ?? "",
        metadata: body.metadata,
        endpoint: body.endpoint,
      });
      this.sendJSON(res, 200, result);
    } catch (error) {
      this.sendJSON(res, 502, { error: normalizeError(error) });
    }
  }

  sendJSON(res, status, body) {
    if (res.headersSent) return;
    res.writeHead(status, { "content-type": "application/json" });
    res.end(JSON.stringify(body));
  }
}

async function readJSONBody(req) {
  let raw = "";
  let size = 0;
  for await (const chunk of req) {
    size += chunk.length;
    if (size > MAX_BODY_BYTES) {
      throw new Error("helper request body is too large");
    }
    raw += chunk.toString("utf8");
  }
  if (!raw.trim()) return {};
  return JSON.parse(raw);
}
