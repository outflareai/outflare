import { createClient, type ApiError } from "@outflare/sdk";
import { track } from "./lib/analytics";

// Same-origin client: the engine serves this app, so the session cookie applies.
export const api = createClient({ clientName: "web" });

// A call that got no answer at all never reaches the server's events.
api.use({
  onError({ error, request }) {
    track("ui.request_failed", {
      outcome: "error",
      error_code: navigator.onLine ? "network_error" : "offline",
      method: request.method,
      name: error instanceof Error ? error.name : "error",
    });
  },
});

// The message to show for a failed call. Showing one is an event: which
// errors people see, and where, is what tells us what frustrates them.
export function errorMessage(error: unknown): string {
  const e = error as ApiError | undefined;
  track("ui.error_shown", { outcome: "failure", error_code: e?.error?.code ?? "unknown" });
  return e?.error?.message ?? "Something went wrong";
}
