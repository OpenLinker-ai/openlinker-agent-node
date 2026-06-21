import { pathToFileURL } from "node:url";
import { AgentNodeError } from "../errors.mjs";

export function createFunctionAdapter(handle) {
  if (typeof handle !== "function") {
    throw new AgentNodeError("adapter handle must be a function", { code: "ADAPTER_CONFIG_ERROR" });
  }
  return {
    async run(input, ctx) {
      return handle(input, ctx);
    },
  };
}

export async function createModuleAdapter({ modulePath, exportName = "handle" } = {}) {
  if (!modulePath) {
    throw new AgentNodeError("modulePath is required", { code: "ADAPTER_CONFIG_ERROR" });
  }
  const url = modulePath.startsWith("file:") || /^https?:\/\//i.test(modulePath)
    ? modulePath
    : pathToFileURL(modulePath).href;
  const mod = await import(url);
  const handle = mod[exportName];
  if (typeof handle !== "function") {
    throw new AgentNodeError(`module export ${exportName} must be a function`, { code: "ADAPTER_CONFIG_ERROR" });
  }
  return createFunctionAdapter(handle);
}
