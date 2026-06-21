import { setTimeout as sleepTimeout } from "node:timers/promises";

export function stripTrailingSlash(value) {
  return String(value ?? "").replace(/\/+$/, "");
}

export function joinAPIPath(apiBase, pathName) {
  if (/^https?:\/\//i.test(pathName)) return pathName;
  return `${stripTrailingSlash(apiBase)}${pathName.startsWith("/") ? "" : "/"}${pathName}`;
}

export function websocketURL(apiBase, pathName) {
  const joined = joinAPIPath(apiBase, pathName);
  if (joined.startsWith("https://")) return `wss://${joined.slice("https://".length)}`;
  if (joined.startsWith("http://")) return `ws://${joined.slice("http://".length)}`;
  return joined;
}

export function sleep(ms, signal) {
  return sleepTimeout(ms, undefined, { signal }).catch((error) => {
    if (error?.name === "AbortError") return undefined;
    throw error;
  });
}

export async function readJSONResponse(res) {
  const text = await res.text();
  if (!text) return {};
  try {
    return JSON.parse(text);
  } catch {
    return { raw: text };
  }
}

export function retryAfterMs(res, fallbackSeconds) {
  const parsed = Number(res.headers.get("retry-after") ?? fallbackSeconds);
  const seconds = Number.isFinite(parsed) && parsed > 0 ? parsed : fallbackSeconds;
  return seconds * 1000;
}

export function numberOption(value, fallback, label) {
  if (value === undefined || value === null || value === "") return fallback;
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed < 0) {
    throw new Error(`${label} must be a non-negative number`);
  }
  return parsed;
}

export function boolOption(value, fallback = false) {
  if (value === undefined || value === null || value === "") return fallback;
  return ["1", "true", "yes", "on"].includes(String(value).toLowerCase());
}

export function parseJSONOption(value, fallback, label) {
  if (value === undefined || value === null || value === "") return fallback;
  try {
    return JSON.parse(value);
  } catch (error) {
    throw new Error(`${label} must be valid JSON: ${error.message}`);
  }
}
