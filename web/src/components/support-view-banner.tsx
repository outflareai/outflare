import { useState } from "react";
import { api, errorMessage } from "../api";
import { toast } from "../ui";

// Shown while a staff member views someone's account (support access): it
// is read-only, mail stays hidden, and here is the way back. Where to go
// back to is left in sessionStorage by whatever started the view, such as
// Cloud's staff console.
export function SupportViewBanner({ name }: { name: string }) {
  const [busy, setBusy] = useState(false);
  async function end() {
    setBusy(true);
    const { error } = await api.DELETE("/v1/me/support-view");
    if (error) {
      setBusy(false);
      return toast(errorMessage(error), "error");
    }
    let back = "/";
    try {
      back = sessionStorage.getItem("outflare_view_return") || "/";
      sessionStorage.removeItem("outflare_view_return");
    } catch {
      // Storage can be off; home is fine.
    }
    // Only a path on this site: "//host" would leave it.
    location.assign(back.startsWith("/") && !back.startsWith("//") && !back.startsWith("/\\") ? back : "/");
  }
  return (
    <div role="status" className="sticky top-0 z-20 mb-6 flex flex-wrap items-center justify-between gap-3 rounded-lg border border-violet-600/30 bg-violet-500/10 px-3 py-2 text-[13px] text-violet-900 backdrop-blur dark:border-violet-400/20 dark:text-violet-200">
      <span>
        You're viewing <strong className="font-medium">{name}</strong>'s account as staff. It's read-only, and their inbox and email content are hidden.
      </span>
      <button onClick={end} disabled={busy} className="font-medium underline underline-offset-4 disabled:opacity-50">
        {busy ? "Ending…" : "End view"}
      </button>
    </div>
  );
}
