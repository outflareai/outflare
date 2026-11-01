import type { Mailbox } from "@outflare/sdk";

// warmupPlatform is what the sequencer running a mailbox's warm-up reports
// about it: its platforms entry for warmup.provider.
export function warmupPlatform(m: Mailbox) {
  return m.warmup.provider == null ? undefined : (m.platforms ?? []).find((p) => p.integration_id === m.warmup.provider);
}

// warmupScore is the running sequencer's warm-up score, 0 to 100, or null.
export function warmupScore(m: Mailbox): number | null {
  return warmupPlatform(m)?.warmup_score ?? null;
}
