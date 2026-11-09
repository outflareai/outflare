// Approvals, agent runs and request logs are each one brand's, on the
// brand's own routes, or the team's (about no brand, such as invites and
// connections), on the workspace's. These pick the routes for a page that
// shows one or the other: a brand id, or none for the team's.

import { api } from "../api";

type ActionQuery = { status?: string; token_id?: string; cursor?: string; limit?: number };
type Note = { note?: string };
type Remember = { fields?: string[]; daily_limit?: number };

export function actionsAPI(ws: string, brand?: string) {
  return {
    list: (query: ActionQuery) =>
      brand ? api.GET("/v1/brands/{brand}/actions", { params: { path: { brand }, query } }) : api.GET("/v1/workspaces/{ws}/actions", { params: { path: { ws }, query } }),
    get: (id: string) =>
      brand ? api.GET("/v1/brands/{brand}/actions/{id}", { params: { path: { brand, id } } }) : api.GET("/v1/workspaces/{ws}/actions/{id}", { params: { path: { ws, id } } }),
    approve: (id: string, body: Note & { remember?: Remember }) =>
      brand ? api.POST("/v1/brands/{brand}/actions/{id}/approve", { params: { path: { brand, id } }, body }) : api.POST("/v1/workspaces/{ws}/actions/{id}/approve", { params: { path: { ws, id } }, body }),
    reject: (id: string, body: Note) =>
      brand ? api.POST("/v1/brands/{brand}/actions/{id}/reject", { params: { path: { brand, id } }, body }) : api.POST("/v1/workspaces/{ws}/actions/{id}/reject", { params: { path: { ws, id } }, body }),
    decide: (body: Note & { approve?: string[]; reject?: string[] }) =>
      brand ? api.POST("/v1/brands/{brand}/actions/decide", { params: { path: { brand } }, body }) : api.POST("/v1/workspaces/{ws}/actions/decide", { params: { path: { ws } }, body }),
  };
}

type RunQuery = { status?: string; cursor?: string; limit?: number };

export function runsAPI(ws: string, brand?: string) {
  return {
    list: (query: RunQuery) =>
      brand ? api.GET("/v1/brands/{brand}/agent-runs", { params: { path: { brand }, query } }) : api.GET("/v1/workspaces/{ws}/agent-runs", { params: { path: { ws }, query } }),
    get: (id: string) =>
      brand ? api.GET("/v1/brands/{brand}/agent-runs/{id}", { params: { path: { brand, id } } }) : api.GET("/v1/workspaces/{ws}/agent-runs/{id}", { params: { path: { ws, id } } }),
    change: (id: string, what: "pause" | "resume" | "stop") => {
      if (brand) {
        const p = { params: { path: { brand, id } } };
        return what === "pause" ? api.POST("/v1/brands/{brand}/agent-runs/{id}/pause", p) : what === "resume" ? api.POST("/v1/brands/{brand}/agent-runs/{id}/resume", p) : api.POST("/v1/brands/{brand}/agent-runs/{id}/stop", p);
      }
      const p = { params: { path: { ws, id } } };
      return what === "pause" ? api.POST("/v1/workspaces/{ws}/agent-runs/{id}/pause", p) : what === "resume" ? api.POST("/v1/workspaces/{ws}/agent-runs/{id}/resume", p) : api.POST("/v1/workspaces/{ws}/agent-runs/{id}/stop", p);
    },
    update: (id: string, body: { kind: "instruction"; body: string }) =>
      brand ? api.POST("/v1/brands/{brand}/agent-runs/{id}/updates", { params: { path: { brand, id } }, body }) : api.POST("/v1/workspaces/{ws}/agent-runs/{id}/updates", { params: { path: { ws, id } }, body }),
  };
}

// Log filters as the API takes them, all strings (see Logs.tsx).
type LogQuery = { [k: string]: string | number | undefined };

export function logsAPI(ws: string, brand?: string) {
  // The generated types list each filter; the page builds them as strings.
  const q = (query: LogQuery & { limit?: number; cursor?: string; start?: "latest" }) => query as never;
  return {
    list: (query: LogQuery & { limit?: number; cursor?: string; start?: "latest" }) =>
      brand ? api.GET("/v1/brands/{brand}/logs", { params: { path: { brand }, query: q(query) } }) : api.GET("/v1/workspaces/{ws}/logs", { params: { path: { ws }, query: q(query) } }),
    histogram: (query: LogQuery) =>
      brand ? api.GET("/v1/brands/{brand}/logs/histogram", { params: { path: { brand }, query: q(query) } }) : api.GET("/v1/workspaces/{ws}/logs/histogram", { params: { path: { ws }, query: q(query) } }),
    get: (id: string) =>
      brand ? api.GET("/v1/brands/{brand}/logs/{id}", { params: { path: { brand, id } } }) : api.GET("/v1/workspaces/{ws}/logs/{id}", { params: { path: { ws, id } } }),
  };
}

// The base path of the request logs, for the export link.
export const logsBase = (ws: string, brand?: string) => (brand ? `/v1/brands/${encodeURIComponent(brand)}` : `/v1/workspaces/${encodeURIComponent(ws)}`);
