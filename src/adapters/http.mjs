import { AgentNodeError } from "../errors.mjs";
import { readJSONResponse } from "../util.mjs";

export function createHTTPAdapter({
  url,
  headers = {},
  fetchImpl = globalThis.fetch,
  timeoutMs = 15 * 60_000,
} = {}) {
  if (!url) throw new AgentNodeError("url is required", { code: "ADAPTER_CONFIG_ERROR" });
  if (!fetchImpl) throw new AgentNodeError("fetch is unavailable", { code: "ADAPTER_CONFIG_ERROR" });
  return {
    async run(input, ctx) {
      const controller = new AbortController();
      const timer = setTimeout(() => controller.abort(), timeoutMs);
      try {
        const res = await fetchImpl(url, {
          method: "POST",
          headers: {
            ...headers,
            "content-type": "application/json",
          },
          signal: controller.signal,
          body: JSON.stringify({
            input,
            run_id: ctx.runId,
            metadata: ctx.metadata,
            a2a: ctx.a2a,
          }),
        });
        const json = await readJSONResponse(res);
        if (!res.ok) {
          throw new AgentNodeError(`HTTP adapter returned ${res.status}`, {
            code: "HTTP_ADAPTER_FAILED",
            status: res.status,
            data: json,
          });
        }
        return json?.output === undefined ? json : json.output;
      } finally {
        clearTimeout(timer);
      }
    },
  };
}
