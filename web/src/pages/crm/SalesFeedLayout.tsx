import { useEffect, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { ArrowDown, ArrowUp, X } from "lucide-react";
import { api, errorMessage } from "@/api";
import { Button, ErrorText, Loading, toast } from "@/ui";
import { track } from "@/lib/analytics";
import { salesChecked as checked } from "@/lib/sales";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

type Channel = Schemas["SalesFeedChannel"];
type Offered = { label: string; posts: number };
const most = 6;
const same = (a: string, b: string) =>
  a
    .trim()
    .replace(/[:?]+$/, "")
    .trim()
    .toLowerCase() ===
  b
    .trim()
    .replace(/[:?]+$/, "")
    .trim()
    .toLowerCase();

// What a channel's posts show first: the team ticks the fields that matter
// for this source and puts them in order. Labels come from the channel's own
// posts in Slack and are shown as text.
export function SalesFeedLayoutDialog({
  brand,
  channel,
  onClose,
  onSaved,
}: {
  brand: string;
  channel: Channel;
  onClose: () => void;
  onSaved: () => unknown;
}) {
  const [offered, setOffered] = useState<Offered[]>();
  const [chosen, setChosen] = useState<string[]>(channel.preview_fields ?? []);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const path = { brand, id: channel.id };
  useEffect(() => {
    let cancelled = false;
    api
      .GET("/v1/brands/{brand}/sales/feeds/{id}/fields", { params: { path } })
      .then((r) => {
        if (cancelled) return;
        const d = checked<{ data: Offered[]; preview_fields: string[] }>(r);
        setOffered(d.data);
        setChosen(d.preview_fields);
        track("ui.sales_feed_layout_opened", {
          labels: d.data.length,
          chosen: d.preview_fields.length,
        });
      })
      .catch((e) => !cancelled && setError(errorMessage(e)));
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [brand, channel.id]);
  const move = (i: number, by: number) =>
    setChosen((all) => {
      const next = [...all];
      const [label] = next.splice(i, 1);
      next.splice(i + by, 0, label);
      return next;
    });
  async function save() {
    setBusy(true);
    setError("");
    try {
      checked(
        await api.PUT("/v1/brands/{brand}/sales/feeds/{id}/layout", {
          params: { path },
          body: { preview_fields: chosen },
        }),
      );
      toast(`#${channel.channel_name} updated.`);
      onSaved();
      onClose();
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  const rest = (offered ?? []).filter(
    (o) => !chosen.some((c) => same(c, o.label)),
  );
  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>Shown first in #{channel.channel_name}</DialogTitle>
          <DialogDescription>
            Pick the fields that matter for this source, in the order you want
            to read them. The queue shows the first four; the conversation page
            shows all of them first. Everyone on the team sees the same.
          </DialogDescription>
        </DialogHeader>
        <ErrorText>{error}</ErrorText>
        {!offered && !error ? (
          <Loading />
        ) : (
          <div className="space-y-4 text-sm">
            <section className="space-y-2">
              <h3 className="text-xs font-medium text-muted-foreground">
                Shown first
              </h3>
              {chosen.length === 0 ? (
                <p className="rounded-lg border border-dashed p-3 text-muted-foreground">
                  Nothing chosen: Outflare shows the first three answers that
                  are not the person’s name or contact details.
                </p>
              ) : (
                <ol className="divide-y rounded-lg border">
                  {chosen.map((label, i) => (
                    <li key={label} className="flex items-center gap-2 p-2">
                      <span className="w-5 text-right text-xs tabular-nums text-muted-foreground">
                        {i + 1}
                      </span>
                      <span className="min-w-0 flex-1 [overflow-wrap:anywhere]">
                        {label}
                        {i >= 4 && (
                          <span className="ml-2 text-xs text-muted-foreground">
                            conversation page only
                          </span>
                        )}
                      </span>
                      <Button
                        variant="ghost"
                        className="h-7 px-1.5"
                        aria-label={`Move ${label} up`}
                        disabled={i === 0}
                        onClick={() => move(i, -1)}
                      >
                        <ArrowUp className="size-3.5" />
                      </Button>
                      <Button
                        variant="ghost"
                        className="h-7 px-1.5"
                        aria-label={`Move ${label} down`}
                        disabled={i === chosen.length - 1}
                        onClick={() => move(i, 1)}
                      >
                        <ArrowDown className="size-3.5" />
                      </Button>
                      <Button
                        variant="ghost"
                        className="h-7 px-1.5"
                        aria-label={`Stop showing ${label} first`}
                        onClick={() =>
                          setChosen((all) => all.filter((l) => l !== label))
                        }
                      >
                        <X className="size-3.5" />
                      </Button>
                    </li>
                  ))}
                </ol>
              )}
            </section>
            <section className="space-y-2">
              <h3 className="text-xs font-medium text-muted-foreground">
                Fields in this channel’s recent posts
              </h3>
              {rest.length === 0 ? (
                <p className="text-muted-foreground">
                  {(offered ?? []).length === 0
                    ? "No fields found yet. They appear once the channel has posts with “Label: value” lines."
                    : "Every field is already shown first."}
                </p>
              ) : (
                <ul className="flex flex-wrap gap-2">
                  {rest.map((o) => (
                    <li key={o.label}>
                      <button
                        type="button"
                        disabled={chosen.length >= most}
                        title={`In ${o.posts} of the recent posts`}
                        className="max-w-full rounded-full border px-3 py-1 text-left text-xs hover:bg-muted/50 disabled:opacity-50 [overflow-wrap:anywhere]"
                        onClick={() => setChosen((all) => [...all, o.label])}
                      >
                        + {o.label}
                      </button>
                    </li>
                  ))}
                </ul>
              )}
              {chosen.length >= most && (
                <p className="text-xs text-muted-foreground">
                  A channel shows at most {most} fields first.
                </p>
              )}
            </section>
          </div>
        )}
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button disabled={busy || !offered} onClick={() => void save()}>
            {busy ? "Saving…" : "Save"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

type Quality = Schemas["SalesFeeds"]["source_quality"][number];

// How reps marked the last 30 days' conversations, per source.
export function SalesSourceQuality({ rows }: { rows: Quality[] }) {
  if (rows.length === 0) return null;
  const share = (n: number, of: number) =>
    of === 0 ? "" : ` (${Math.round((n / of) * 100)}%)`;
  return (
    <section className="space-y-2">
      <h3 className="text-sm font-medium">Quality by source</h3>
      <p className="max-w-2xl text-xs text-muted-foreground">
        Conversations of the last 30 days and how the team marked them in the
        queue. Percentages are of the marked ones.
      </p>
      <div className="overflow-x-auto rounded-xl border bg-card">
        <table className="w-full text-left text-sm">
          <thead className="border-b bg-muted/20 text-xs text-muted-foreground">
            <tr>
              <th className="px-3 py-3 font-medium">Source</th>
              {["Conversations", "Good", "Poor fit", "Junk", "Not marked"].map(
                (name) => (
                  <th
                    key={name}
                    className="whitespace-nowrap px-3 py-3 text-right font-medium"
                  >
                    {name}
                  </th>
                ),
              )}
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => {
              const marked = r.good + r.poor_fit + r.junk;
              return (
                <tr
                  key={`${r.source}|${r.reply}`}
                  className="border-b last:border-0"
                >
                  <td className="px-3 py-3 font-medium">
                    {r.source ||
                      (r.reply ? "Email reply" : "Posted by a person")}
                  </td>
                  <td className="px-3 py-3 text-right tabular-nums">
                    {r.total}
                  </td>
                  <td className="px-3 py-3 text-right tabular-nums">
                    {r.good}
                    {share(r.good, marked)}
                  </td>
                  <td className="px-3 py-3 text-right tabular-nums">
                    {r.poor_fit}
                    {share(r.poor_fit, marked)}
                  </td>
                  <td className="px-3 py-3 text-right tabular-nums">
                    {r.junk}
                    {share(r.junk, marked)}
                  </td>
                  <td className="px-3 py-3 text-right tabular-nums text-muted-foreground">
                    {r.total - marked}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </section>
  );
}
