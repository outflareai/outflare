// One word and one colour for each state, used on every page, so a
// mailbox that works says "Sending" everywhere and "Needs attention" is
// always amber. The five tones mean the same thing on every object:
//
//   green  working as it should
//   blue   in progress, nothing to do (checking, warming up, syncing)
//   amber  works, but a person should decide or fix something
//   red    broken, or stopped by an error
//   grey   off by choice, or not started (paused, draft, retired)
//
// A page that shows a state imports it from here instead of writing its
// own label or tone.

import type { components } from "@outflare/sdk";

export type Tone = "green" | "blue" | "amber" | "red" | "grey";
export type State = { label: string; tone: Tone };

type S = components["schemas"];

export const mailboxStatus: Record<S["Mailbox"]["status"], State> = {
  active: { label: "Sending", tone: "green" },
  verifying: { label: "Checking…", tone: "blue" },
  paused: { label: "Paused", tone: "grey" },
  error: { label: "Not sending", tone: "red" },
};

// A domain's or a sending server's health.
export const health: Record<S["SendingDomain"]["health"], State> = {
  healthy: { label: "Healthy", tone: "green" },
  pending: { label: "Checking…", tone: "blue" },
  warning: { label: "Needs attention", tone: "amber" },
  critical: { label: "At risk", tone: "red" },
};

// What to fix on a domain, a server or anything else with issues.
export const issueSeverity: Record<"critical" | "warning" | "info", State> = {
  critical: { label: "Fix now", tone: "red" },
  warning: { label: "Should fix", tone: "amber" },
  info: { label: "Good to know", tone: "grey" },
};

// A connection to another service.
export const connection = {
  ok: { label: "Connected", tone: "green" },
  syncing: { label: "Syncing", tone: "blue" },
  failing: { label: "Not working", tone: "red" },
  paused: { label: "Paused", tone: "grey" },
} satisfies Record<string, State>;

// Sequences, projects and missions share these.
export const runStatus = {
  draft: { label: "Draft", tone: "grey" },
  active: { label: "Sending", tone: "green" },
  running: { label: "Running", tone: "green" },
  pending_approval: { label: "Waiting for approval", tone: "amber" },
  paused: { label: "Paused", tone: "grey" },
  completed: { label: "Done", tone: "blue" },
  stopped: { label: "Stopped", tone: "grey" },
  archived: { label: "Archived", tone: "grey" },
  failed: { label: "Failed", tone: "red" },
} satisfies Record<string, State>;

// Blockers from GET /status, by severity: the Home list colours its dots
// with these, so an unread reply never looks like a broken domain.
export const blockerTone: Record<"critical" | "warning" | "info", Tone> = { critical: "red", warning: "amber", info: "grey" };

export const dotClass: Record<Tone, string> = {
  green: "bg-green-500",
  blue: "bg-blue-500",
  amber: "bg-amber-500",
  red: "bg-red-500",
  grey: "bg-muted-foreground/60",
};
