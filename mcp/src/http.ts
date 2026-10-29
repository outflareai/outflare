// The hosted MCP server: the same tools over Streamable HTTP, for apps that
// connect with a URL (Claude, ChatGPT, Cursor...). The Outflare engine runs
// it next to itself and proxies https://<server>/mcp to it, after checking
// the caller's bearer token (an OAuth access token or an API token), so this
// only listens on loopback and only answers the engine.
//
// Each MCP session keeps its own server, because use_mission changes
// what later calls do. The session belongs to the API
// token that opened it: an app's refreshed access token is the same token
// with a new secret, so it carries on; any other token is refused.

import { randomUUID, timingSafeEqual } from "node:crypto";
import { createServer as createHttpServer, type IncomingMessage, type ServerResponse } from "node:http";
import { StreamableHTTPServerTransport } from "@modelcontextprotocol/sdk/server/streamableHttp.js";
import { isInitializeRequest } from "@modelcontextprotocol/sdk/types.js";
import { createClient } from "@outflare/sdk";
import { createServer, VERSION } from "./server.js";

type Session = {
  transport: StreamableHTTPServerTransport;
  auth: { token: string };
  tokenId: string;
  seen: number;
};

export type HttpOptions = {
  /** host:port to listen on, normally 127.0.0.1:<port>. */
  listen: string;
  /** The engine's own address, for the API calls the tools make. */
  baseUrl: string;
  /** Sent by the engine on every request it proxies. */
  secret: string;
  /** Sessions idle this long are closed; the app opens a new one. */
  idleMs?: number;
};

const bearer = (req: IncomingMessage) => /^Bearer (.+)$/.exec(req.headers.authorization ?? "")?.[1]?.trim() ?? "";

function same(a: string, b: string): boolean {
  const x = Buffer.from(a);
  const y = Buffer.from(b);
  return x.length === y.length && timingSafeEqual(x, y);
}

// data carries the API's error code and param, which the engine records in
// its request log; a JSON-RPC code alone would log as http_400.
function fail(res: ServerResponse, status: number, code: number, message: string, data?: { code: string; param?: string }) {
  res.writeHead(status, { "Content-Type": "application/json" }).end(JSON.stringify({ jsonrpc: "2.0", error: { code, message, ...(data && { data }) }, id: null }));
}

// The JSON-RPC method a refused request named ("tools/list"), so the log
// says what an app sent instead of initialize. Only the name, never more.
const methodOf = (body: unknown) => {
  const m = (Array.isArray(body) ? body[0] : body) as { method?: unknown } | undefined;
  return typeof m?.method === "string" ? m.method.replace(/[^\w./-]/g, "").slice(0, 64) : "";
};

async function readJSON(req: IncomingMessage): Promise<unknown> {
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const c of req) {
    size += (c as Buffer).length;
    if (size > 4 << 20) throw new Error("body too large");
    chunks.push(c as Buffer);
  }
  return chunks.length ? JSON.parse(Buffer.concat(chunks).toString("utf8")) : undefined;
}

type Who = { tokenId: string; workspace: string; scopes: string[] };

export function serveHttp(opts: HttpOptions) {
  const sessions = new Map<string, Session>();
  const idle = opts.idleMs ?? 30 * 60_000;

  // client makes an API client whose token can change under it.
  const client = (auth: { token: string }) => {
    const c = createClient({ baseUrl: opts.baseUrl, token: auth.token, missions: true, clientName: `mcp/${VERSION}` });
    c.use({
      onRequest({ request }) {
        request.headers.set("Authorization", `Bearer ${auth.token}`);
        return request;
      },
    });
    return c;
  };

  async function whoami(token: string): Promise<Who | null> {
    const { data } = await client({ token }).GET("/v1/whoami");
    const t = data?.token;
    if (!t?.workspace_id) return null;
    return { tokenId: t.id, workspace: t.workspace_id, scopes: t.scopes ?? [] };
  }

  async function open(token: string, res: ServerResponse): Promise<Session | null> {
    const who = await whoami(token);
    if (!who) {
      fail(res, 401, -32001, "The token is not valid. Connect the app again.", { code: "unauthenticated" });
      return null;
    }
    const auth = { token };
    // A token that may change nothing gets only the tools that change nothing.
    const readOnly = !who.scopes.some((s) => !s.endsWith(".read"));
    const server = createServer({ client: client(auth), workspace: who.workspace, readOnly });
    let session: Session;
    const transport = new StreamableHTTPServerTransport({
      sessionIdGenerator: () => randomUUID(),
      onsessioninitialized: (id) => {
        sessions.set(id, session);
      },
      onsessionclosed: (id) => {
        sessions.delete(id);
      },
    });
    session = { transport, auth, tokenId: who.tokenId, seen: Date.now() };
    transport.onclose = () => {
      if (transport.sessionId) sessions.delete(transport.sessionId);
    };
    await server.connect(transport);
    return session;
  }

  const http = createHttpServer(async (req, res) => {
    try {
      if (!same(String(req.headers["x-outflare-proxy-secret"] ?? ""), opts.secret)) return fail(res, 403, -32000, "Only the Outflare server may call this.");
      if (new URL(req.url ?? "/", "http://x").pathname !== "/mcp") return fail(res, 404, -32000, "Not found.");
      const token = bearer(req);
      if (!token) return fail(res, 401, -32001, "Send the access token as Authorization: Bearer.", { code: "unauthenticated" });
      const id = req.headers["mcp-session-id"];
      if (typeof id === "string" && id) {
        const s = sessions.get(id);
        // A session this server doesn't know (it restarted, or it timed
        // out): 404 tells the app to start a new one.
        if (!s) return fail(res, 404, -32001, "Session not found. Start a new session.", { code: "not_found", param: "Mcp-Session-Id" });
        if (!same(token, s.auth.token)) {
          const who = await whoami(token);
          if (!who || who.tokenId !== s.tokenId) return fail(res, 403, -32001, "This session belongs to another token.", { code: "forbidden", param: "Mcp-Session-Id" });
          s.auth.token = token;
        }
        s.seen = Date.now();
        const body = req.method === "POST" ? await readJSON(req) : undefined;
        return await s.transport.handleRequest(req, res, body);
      }
      if (req.method !== "POST") return fail(res, 400, -32000, "Start a session with an initialize request.", { code: "invalid_request", param: req.method });
      const body = await readJSON(req);
      if (!isInitializeRequest(body)) return fail(res, 400, -32000, "Start a session with an initialize request.", { code: "invalid_request", param: methodOf(body) || "method" });
      const s = await open(token, res);
      if (!s) return;
      const name = (body as { params?: { clientInfo?: { name?: string } } }).params?.clientInfo?.name;
      // The engine records which app connected; only the name, never more.
      if (name) res.setHeader("X-Outflare-Agent", name.replace(/[^\w .\-/]/g, "").slice(0, 64));
      await s.transport.handleRequest(req, res, body);
    } catch (e) {
      console.error("outflare-mcp http:", e);
      if (!res.headersSent) fail(res, 500, -32603, "Internal error. Try again.");
      else res.end();
    }
  });

  const sweep = setInterval(() => {
    const cutoff = Date.now() - idle;
    for (const [id, s] of sessions) {
      if (s.seen < cutoff) {
        sessions.delete(id);
        s.transport.close().catch(() => {});
      }
    }
  }, 60_000);
  sweep.unref();

  const [host, port] = splitHostPort(opts.listen);
  http.listen(port, host, () => console.error(`outflare-mcp: listening on ${host}:${port}`));
  return { http, sessions, close: () => (clearInterval(sweep), http.close()) };
}

function splitHostPort(s: string): [string, number] {
  const i = s.lastIndexOf(":");
  return [s.slice(0, i) || "127.0.0.1", Number(s.slice(i + 1))];
}
