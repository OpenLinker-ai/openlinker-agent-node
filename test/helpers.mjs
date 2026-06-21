import http from "node:http";

export function listen(server, host = "127.0.0.1", port = 0) {
  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(port, host, () => {
      server.off("error", reject);
      resolve(server.address());
    });
  });
}

export function close(server) {
  return new Promise((resolve) => server.close(resolve));
}

export function createJSONServer(handler) {
  return http.createServer(async (req, res) => {
    try {
      let raw = "";
      for await (const chunk of req) raw += chunk;
      const body = raw ? JSON.parse(raw) : undefined;
      const response = await handler(req, body);
      if (response?.status === 204) {
        res.writeHead(204, response.headers ?? {});
        res.end();
        return;
      }
      res.writeHead(response?.status ?? 200, {
        "content-type": "application/json",
        ...(response?.headers ?? {}),
      });
      res.end(JSON.stringify(response?.body ?? {}));
    } catch (error) {
      res.writeHead(500, { "content-type": "application/json" });
      res.end(JSON.stringify({ error: { message: error.message, stack: error.stack } }));
    }
  });
}

export function waitFor(predicate, { timeoutMs = 3000, intervalMs = 20 } = {}) {
  const start = Date.now();
  return new Promise((resolve, reject) => {
    const tick = () => {
      try {
        const value = predicate();
        if (value) {
          resolve(value);
          return;
        }
      } catch (error) {
        reject(error);
        return;
      }
      if (Date.now() - start >= timeoutMs) {
        reject(new Error("condition not met before timeout"));
        return;
      }
      setTimeout(tick, intervalMs);
    };
    tick();
  });
}
