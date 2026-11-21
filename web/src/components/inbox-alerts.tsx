import { useEffect, useRef, useState } from "react";
import { Bell, BellOff } from "lucide-react";
import type { SupportConversation } from "@outflare/sdk";
import { api } from "../api";
import { pickedBrandId } from "@/lib/brand";
import { track } from "@/lib/analytics";
import { alertNow, alertsOn, askToNotify, canNotify, chime, setAlerts } from "@/lib/notify";
import { toast, toastAction } from "../ui";
import { cn } from "@/lib/utils";

// Sound and notifications, under Online and Away in the Inbox's views
// column. Off until the person turns it on, per browser. On plays a chime
// for a new chat or message that is theirs to answer and, when the page is
// in the background, shows a notification from the browser.
export function AlertsSwitch() {
  const [on, setOn] = useState(alertsOn);
  async function flip() {
    const next = !on;
    setAlerts(next);
    setOn(next);
    track("ui.inbox_alerts_changed", { on: next });
    if (!next) return toast("Sound and notifications are off.");
    chime();
    const allowed = await askToNotify();
    toast(
      allowed
        ? "Sound and notifications are on. You'll hear a new chat, and see a notification when this tab is in the background."
        : canNotify()
          ? "Sound is on. Your browser is blocking notifications for this site: allow them in the address bar to see one when this tab is in the background."
          : "Sound is on. This browser has no notifications.",
    );
  }
  return (
    <button
      type="button"
      aria-pressed={on}
      onClick={flip}
      title={on ? "Turn off the sound and notifications for new chats and messages" : "Play a sound and show a notification for new chats and messages"}
      className={cn("mx-2 mb-3 flex items-center gap-1.5 rounded-md border px-2 py-1 text-xs", on ? "bg-background text-foreground" : "text-muted-foreground hover:bg-accent")}
    >
      {on ? <Bell className="size-3.5" aria-hidden /> : <BellOff className="size-3.5" aria-hidden />}
      <span className="flex-1 text-left">Sound and notifications</span>
      <span className="font-medium">{on ? "On" : "Off"}</span>
    </button>
  );
}

// Watches for something new to answer: every 15 seconds, the conversations
// waiting on the team. One that is new, or has a newer message from the
// customer, and is this person's or nobody's, shows a message with an Open
// button and sounds the alert. onChange lets the list show it straight away.
export function useNewConversations(myId: string, open: (id: string) => void, onChange: () => void) {
  const known = useRef<Map<string, string> | null>(null);
  const fns = useRef({ open, onChange });
  fns.current = { open, onChange };
  const brand = pickedBrandId();

  useEffect(() => {
    if (!myId || !brand) return;
    known.current = null;
    let live = true;
    const load = async () => {
      if (document.visibilityState === "hidden" && !alertsOn()) return;
      const { data } = await api.GET("/v1/brands/{brand}/support/conversations", { params: { path: { brand }, query: { status: "open", limit: 30 } as never } });
      if (!live || !data) return;
      const first = known.current === null;
      const seen = known.current ?? new Map<string, string>();
      let changed = false;
      for (const c of data.data as SupportConversation[]) {
        const was = seen.get(c.id);
        if (was === c.last_message_at) continue;
        seen.set(c.id, c.last_message_at);
        if (first) continue;
        changed = true;
        if (c.last_from !== "customer" || !c.unread || (c.assignee && c.assignee.id !== myId)) continue;
        const who = c.contact.name || c.contact.email || "a visitor";
        const text = was ? `New message from ${who}.` : c.channel === "chat" ? `New chat from ${who}.` : `New conversation from ${who}.`;
        toastAction(text, "Open", () => fns.current.open(c.id));
        alertNow(was ? "New message" : c.channel === "chat" ? "New chat" : "New conversation", text, () => fns.current.open(c.id));
      }
      known.current = seen;
      if (changed) fns.current.onChange();
    };
    load();
    const t = setInterval(load, 15_000);
    return () => {
      live = false;
      clearInterval(t);
    };
  }, [myId, brand]);
}
