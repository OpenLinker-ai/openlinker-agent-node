export class AgentNodeError extends Error {
  constructor(message, options = {}) {
    super(message);
    this.name = "AgentNodeError";
    this.code = options.code ?? "AGENT_NODE_ERROR";
    this.status = options.status;
    this.data = options.data;
  }
}

export function normalizeError(error, fallbackCode = "AGENT_BACKEND_ERROR") {
  if (error && typeof error === "object" && "code" in error && "message" in error) {
    return {
      code: String(error.code),
      message: String(error.message),
    };
  }
  return {
    code: fallbackCode,
    message: error instanceof Error ? error.message : String(error),
  };
}
