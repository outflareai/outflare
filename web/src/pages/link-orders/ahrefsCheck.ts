import { api, errorMessage } from "../../api";
import { toast } from "../../ui";
import { track } from "../../lib/analytics";
import type { PageData } from "./shared";

/**
 * checkInAhrefs queues an Ahrefs check of catalog sites on the brand's own
 * Ahrefs connection (about 250 API units a site) and reloads the page
 * once it has had time to finish. Sites checked in the last 30 days are
 * not checked again (the server skips them as fresh). True when a check
 * was queued.
 */
export async function checkInAhrefs(data: PageData, ids: string[], from: "picks" | "order"): Promise<boolean> {
  if (!ids.length) return false;
  // The API takes up to 50 sites a request.
  const res = { checks: 0, fresh: 0, estimated_units: 0 };
  for (let at = 0; at < ids.length; at += 50) {
    const { data: part, error } = await api.POST("/v1/brands/{brand}/link-orders/catalog/ahrefs-check", {
      params: { path: { brand: data.brand } },
      body: { ids: ids.slice(at, at + 50) },
    });
    if (error || !part) {
      toast(errorMessage(error));
      if (!res.checks) return false;
      break;
    }
    if (!("checks" in part)) {
      toast("Held for approval: workspace changes are held right now.");
      return false;
    }
    res.checks += part.checks;
    res.fresh += part.fresh;
    res.estimated_units += part.estimated_units;
  }
  track(from === "picks" ? "ui.link_order_picks_ahrefs_checked" : "ui.link_order_order_ahrefs_checked", { count: res.checks });
  const fresh = res.fresh ? ` ${res.fresh} checked in the last 30 days: their stored numbers are used, at no cost.` : "";
  if (!res.checks) {
    toast(`Nothing to check in Ahrefs.${fresh}`);
    return false;
  }
  toast(`Checking ${res.checks} site${res.checks === 1 ? "" : "s"} in Ahrefs (about ${res.estimated_units.toLocaleString()} API units). The light updates when it finishes.${fresh}`);
  window.setTimeout(() => data.reload(), 20_000);
  return true;
}
