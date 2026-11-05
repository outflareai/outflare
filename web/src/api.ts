import { createClient, type ApiError } from "@outflare/sdk";
import { track } from "./lib/analytics";

// Same-origin client: the engine serves this app, so the session cookie applies.
export const api = createClient({ clientName: "web" });

// A call that got no answer at all (offline, a dropped connection, a deploy
// restart) never reaches the server's events, so it is recorded here. It then
// comes back to the caller as an ordinary { error } answer with the code
// network_error or offline, like any other API error, instead of throwing: a
// page that only handles { error } shows a message rather than a spinner that
// never ends. A cancelled call (AbortError) still throws, so a page that
// cancels a call it no longer needs never shows an error for it.
api.use({
  onError({ error, request }) {
    if (error instanceof Error && error.name === "AbortError") return;
    const code = navigator.onLine ? "network_error" : "offline";
    track("ui.request_failed", {
      outcome: "error",
      error_code: code,
      method: request.method,
      name: error instanceof Error ? error.name : "error",
    });
    return networkError(code);
  },
});

// The answer a call gets when it got none: the API's error shape, so
// errorMessage() shows it. 503 because nothing answered; request_id is empty
// because the request never reached a server.
export function networkError(code: "network_error" | "offline"): Response {
  const message = code === "offline" ? "You're offline. Check your connection and try again." : "Couldn't reach Outflare. Check your connection and try again.";
  return new Response(JSON.stringify({ error: { code, message, request_id: "" } }), { status: 503, headers: { "Content-Type": "application/json" } });
}

// The message to show for a failed call. Showing one is an event: which
// errors people see, and where, is what tells us what frustrates them.
export function errorMessage(error: unknown): string {
  const e = error as ApiError | undefined;
  track("ui.error_shown", { outcome: "failure", error_code: e?.error?.code ?? "unknown" });
  return e?.error?.message ?? "Something went wrong";
}

// Every workspace read carries the brand picked in the sidebar (lib/brand.ts),
// so no page shows another brand's records by forgetting to ask for its
// own. A call that names a brand itself (brand: "all" for every brand)
// keeps it. Connecting a tool and the company context take brand to mean
// something else, so they are left alone.
let requestBrand = "";
export function setRequestBrand(handle: string) {
  requestBrand = handle;
}
const ownBrandParam = [/\/integrations\/oauth\//, /\/context$/];

api.use({
  onRequest({ request }) {
    if (!requestBrand || request.method !== "GET") return;
    const url = new URL(request.url);
    if (!/^\/v1\/workspaces\/[^/]+\/./.test(url.pathname) || url.searchParams.has("brand") || ownBrandParam.some((r) => r.test(url.pathname))) return;
    url.searchParams.set("brand", requestBrand);
    return new Request(url, request);
  },
});
