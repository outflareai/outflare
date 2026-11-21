import { useEffect, useState } from "react";
import { api, errorMessage } from "../../api";
import { appPath } from "../../lib/paths";
import { Badge } from "../../ui";
import { day, type PageData } from "./shared";
import type { components } from "@outflare/sdk";

type Thread = components["schemas"]["LinkOrderThread"];

// The brand's emails with an order's donor, beside the order: what was
// sent, what they replied (with its Inbox label) and the prices written in
// the replies, to fill in the quote without copying between modules. It
// reads stored mail; replies are written from the Inbox.

export function OrderThread({ data, orderId }: { data: PageData; orderId: string }) {
  const canRead = data.can("inbox.read");
  const [thread, setThread] = useState<Thread | null>(null);
  const [err, setErr] = useState("");
  const [open, setOpen] = useState<string | null>(null);

  useEffect(() => {
    if (!canRead) return;
    let gone = false;
    setThread(null);
    setErr("");
    api.GET("/v1/brands/{brand}/link-orders/{id}/thread", { params: { path: { brand: data.brand, id: orderId } } }).then(({ data: t, error }) => {
      if (gone) return;
      if (error || !t) setErr(errorMessage(error));
      else setThread(t);
    });
    return () => {
      gone = true;
    };
  }, [data.brand, orderId, canRead]);

  return (
    <section className="flex flex-col gap-2" aria-label="Conversation with the donor">
      <h3 className="text-[13px] font-semibold">Conversation</h3>
      {!canRead ? (
        <p className="text-[12px] text-muted-foreground">Seeing the emails needs Inbox access (inbox.read).</p>
      ) : err ? (
        <p className="text-[12px] text-destructive">{err}</p>
      ) : !thread ? (
        <p className="text-[12px] text-muted-foreground">Loading…</p>
      ) : thread.messages.length === 0 ? (
        <p className="text-[12px] text-muted-foreground">
          No emails with {thread.domain}{thread.addresses.length ? ` or ${thread.addresses.join(", ")}` : ""} yet. Send the site to a sequence from the catalog, and replies show up here.
        </p>
      ) : (
        <ol className="flex flex-col gap-2">
          {thread.messages.map((m) => (
            <li key={m.id} className={`rounded-md border p-2 text-[12px] ${m.direction === "received" ? "bg-accent/30" : ""}`}>
              <button type="button" className="w-full text-left" aria-expanded={open === m.id} onClick={() => setOpen(open === m.id ? null : m.id)}>
                <div className="flex flex-wrap items-center gap-1.5">
                  <span className="font-medium">{m.direction === "sent" ? `To ${m.to}` : `From ${m.from}`}</span>
                  <span className="text-muted-foreground">{day(m.at)}</span>
                  {m.category ? <Badge tone={m.category === "interested" ? "green" : "grey"}>{m.category.replaceAll("_", " ")}</Badge> : null}
                  {m.kind === "bounce" || m.kind === "auto_reply" ? <Badge tone="amber">{m.kind.replace("_", " ")}</Badge> : null}
                  {m.amounts.map((a) => <Badge key={a} tone="blue" title="A price written in the reply, not confirmed">{a}</Badge>)}
                </div>
                <div className="truncate">{m.subject || "(no subject)"}</div>
              </button>
              {open === m.id ? <p className="mt-1 whitespace-pre-wrap text-muted-foreground">{m.body}</p> : null}
            </li>
          ))}
        </ol>
      )}
      {canRead ? (
        <a className="text-[12px] text-muted-foreground underline hover:text-foreground" href={appPath(data.ws, "inbox")}>
          Reply from the Inbox
        </a>
      ) : null}
    </section>
  );
}
