// Why a daily read (a review page, a social profile) failed, in words, by
// its last_error_code, and when the engine tries again on its own: the
// same day (engine/internal/retry), so the day-by-day numbers stay whole.
export const readError: Record<string, string> = {
  blocked: "The site showed a bot check instead of the page.",
  page_not_found: "The site says this page doesn't exist. Check the address in the brand's links.",
  invalid_address: "This address isn't one Outflare can read. Check it in the brand's links.",
  not_a_review_page: "The page showed no reviews or rating. The site may have changed its page; Outflare's team is told.",
  no_follower_count: "The page showed no follower count: it may be private, suspended or behind a login.",
  save_failed: "The page was read but couldn't be saved. Outflare's team is told.",
  connector_auth: "The connection refused its key. Reconnect it in Connectors.",
  connector_quota: "The connected account is out of credits or rate limited.",
  connector_unavailable: "The connection couldn't read the page just now.",
};

// readWhy is a failed read's reason in words.
export const readWhy = (code: string | null | undefined, message: string) => (code && readError[code]) || message;

// retryIn says when the next automatic try is, in words.
export function retryIn(iso: string): string {
  const mins = Math.max(0, Math.round((new Date(iso).getTime() - Date.now()) / 60000));
  if (mins < 2) return "in a minute";
  if (mins < 90) return `in ${mins} minutes`;
  if (mins < 36 * 60) return `in ${Math.round(mins / 60)} hours`;
  return `in ${Math.round(mins / 1440)} days`;
}
