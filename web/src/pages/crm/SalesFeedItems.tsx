import { useCallback, useEffect, useRef, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { ArrowLeft, ArrowRight, ExternalLink } from "lucide-react";
import { api, errorMessage } from "@/api";
import { Button, Empty, ErrorText, Loading, field, toast } from "@/ui";
import { track } from "@/lib/analytics";
import { salesChecked as checked, salesExternalURL } from "@/lib/sales";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

type Item = Schemas["SalesFeedItem"];
export type SalesFeedStatus = "needs_look" | "spam" | "failed";
type Status = SalesFeedStatus;
type Act = Schemas["SalesFeedItemAction"];

const titles: Record<Status, [string, string, string, string]> = {
  needs_look: [
    "Needs a look",
    "Posts from Sales channels where Outflare couldn't tell who wrote in. Say who it is, or set the post aside.",
    "Nothing needs a look",
    "Posts where Outflare can't tell who wrote in show up here.",
  ],
  spam: [
    "Spam",
    "Posts from Sales channels the AI was sure are spam. Mark one as not spam to read it as a normal post.",
    "No spam",
    "Posts the AI is sure are spam show up here.",
  ],
  failed: [
    "Failed",
    "Posts from Sales channels Outflare couldn't read, even after trying again. Say who wrote in to read the post with them, or set it aside.",
    "Nothing failed",
    "Posts Outflare couldn't read show up here.",
  ],
};

// The Slack posts from Sales channels that wait for a person's decision.
// Post text comes from Slack and is shown as text, never as markup.
export function SalesFeedItems({
  brand,
  status,
  feedChannel,
  onAllChannels,
  onBack,
}: {
  brand: string;
  status: Status;
  feedChannel?: string;
  onAllChannels: () => void;
  onBack: () => void;
}) {
  const [items, setItems] = useState<Item[]>();
  const [next, setNext] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [acting, setActing] = useState<string>();
  const [person, setPerson] = useState<Item>();
  const busy = useRef(false);

  const load = useCallback(
    async (cursor?: string) => {
      const data = checked<Schemas["SalesFeedItemList"]>(
        await api.GET("/v1/brands/{brand}/sales/feed-items", {
          params: {
            path: { brand },
            query: { status, feed_channel: feedChannel, cursor, limit: 50 },
          },
        }),
      );
      setItems((all) => (cursor ? [...(all ?? []), ...data.data] : data.data));
      setNext(data.next_cursor);
    },
    [brand, status, feedChannel],
  );
  useEffect(() => {
    setItems(undefined);
    setError("");
    void load().catch((e) => setError(errorMessage(e)));
  }, [load]);

  // Sends one decision; the post leaves the list once it's made.
  async function act(item: Item, body: Act) {
    const result = checked<Item>(
      await api.POST("/v1/brands/{brand}/sales/feed-items/{id}/actions", {
        params: { path: { brand, id: item.id } },
        body,
      }),
    );
    setItems((all) => all?.filter((i) => i.id !== item.id));
    return result;
  }
  async function quick(item: Item, action: "not_spam" | "ignore") {
    if (busy.current) return;
    busy.current = true;
    setActing(item.id);
    setError("");
    try {
      await act(item, { action });
      toast(action === "ignore" ? "Post set aside." : "Post read again.");
    } catch (e) {
      const message = errorMessage(e);
      setError(message);
      toast(message, "error");
    } finally {
      busy.current = false;
      setActing(undefined);
    }
  }

  const [title, intro, emptyTitle, empty] = titles[status];
  const channelName = feedChannel ? items?.[0]?.channel_name : undefined;
  return (
    <>
      <Button variant="ghost" className="-ml-2" onClick={onBack}>
        <ArrowLeft className="size-3.5" /> Back to Sales channels
      </Button>
      <h2 className="text-2xl font-semibold">{title}</h2>
      <p className="max-w-2xl text-sm text-muted-foreground">{intro}</p>
      {feedChannel && (
        <p className="flex items-center gap-2 text-sm">
          <span>Only {channelName ? `#${channelName}` : "one channel"}</span>
          <Button variant="ghost" onClick={onAllChannels}>
            Show every channel
          </Button>
        </p>
      )}
      <ErrorText>{error}</ErrorText>
      {items === undefined ? (
        error ? null : (
          <Loading />
        )
      ) : items.length === 0 ? (
        <Empty title={emptyTitle}>{empty}</Empty>
      ) : (
        <div className="overflow-x-auto rounded-xl border bg-card">
          <table className="w-full text-left text-sm">
            <thead className="border-b bg-muted/20 text-xs text-muted-foreground">
              <tr>
                <th className="px-3 py-3 font-medium">When</th>
                <th className="px-3 py-3 font-medium">Channel</th>
                <th className="min-w-64 px-3 py-3 font-medium">Post</th>
                <th className="px-3 py-3 font-medium">Addresses found</th>
                <th className="px-3 py-3">
                  <span className="sr-only">Post actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {items.map((item) => {
                const link = salesExternalURL(item.link);
                return (
                  <tr
                    key={item.id}
                    className="border-b align-top last:border-0"
                  >
                    <td className="whitespace-nowrap px-3 py-3 text-xs text-muted-foreground">
                      {new Date(item.occurred_at).toLocaleString()}
                    </td>
                    <td className="whitespace-nowrap px-3 py-3">
                      #{item.channel_name}
                    </td>
                    <td className="px-3 py-3">
                      {item.summary && (
                        <p className="mb-1 font-medium">{item.summary}</p>
                      )}
                      <p className="line-clamp-3 whitespace-pre-wrap break-words text-muted-foreground">
                        {item.text}
                      </p>
                      {link && (
                        <a
                          href={link}
                          target="_blank"
                          rel="noreferrer noopener"
                          className="mt-1 inline-flex items-center gap-1 text-xs underline underline-offset-2"
                        >
                          Open in Slack <ExternalLink className="size-3" />
                        </a>
                      )}
                    </td>
                    <td className="px-3 py-3 text-xs">
                      {item.candidates.length
                        ? item.candidates.map((c) => (
                            <div key={c} className="break-all">
                              {c}
                            </div>
                          ))
                        : "None"}
                    </td>
                    <td className="whitespace-nowrap px-3 py-3 text-right">
                      {status !== "spam" ? (
                        <Button
                          variant="ghost"
                          disabled={acting === item.id}
                          onClick={() => {
                            setPerson(item);
                            track("ui.sales_feed_item_opened", {
                              status,
                              candidates: item.candidates.length,
                            });
                          }}
                        >
                          This is the person
                        </Button>
                      ) : (
                        <Button
                          variant="ghost"
                          disabled={acting === item.id}
                          onClick={() => void quick(item, "not_spam")}
                        >
                          Not spam
                        </Button>
                      )}
                      <Button
                        variant="ghost"
                        disabled={acting === item.id}
                        onClick={() => void quick(item, "ignore")}
                      >
                        Ignore
                      </Button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {next && (
        <Button
          variant="ghost"
          onClick={() =>
            void load(next).catch((e) => setError(errorMessage(e)))
          }
        >
          More posts <ArrowRight className="size-3.5" />
        </Button>
      )}
      {person && (
        <PersonDialog
          item={person}
          onClose={() => setPerson(undefined)}
          onAssign={async (email) => {
            await act(person, { action: "assign_person", email });
            setPerson(undefined);
            toast("Person saved. The post is read with them.");
          }}
        />
      )}
    </>
  );
}

// Who wrote in: one of the addresses found in the post, or another one.
function PersonDialog({
  item,
  onClose,
  onAssign,
}: {
  item: Item;
  onClose: () => void;
  onAssign: (email: string) => Promise<void>;
}) {
  const [picked, setPicked] = useState(item.candidates[0] ?? "");
  const [other, setOther] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const email = (picked || other).trim();
  async function submit() {
    if (busy || !email) return;
    setBusy(true);
    setError("");
    try {
      await onAssign(email);
    } catch (e) {
      setError(errorMessage(e));
      setBusy(false);
    }
  }
  return (
    <Dialog open onOpenChange={(v) => !v && !busy && onClose()}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>This is the person</DialogTitle>
          <DialogDescription>
            Choose who wrote in. Outflare reads the post with them, as if their
            address had been in it.
          </DialogDescription>
        </DialogHeader>
        <div className="max-h-48 overflow-y-auto rounded-lg border bg-muted/20 p-3 text-xs">
          {item.fields.length > 0 && (
            <dl className="mb-2 grid grid-cols-[minmax(6rem,auto)_1fr] gap-x-3 gap-y-1">
              {item.fields.map((f, i) => (
                <div key={i} className="contents">
                  <dt className="text-muted-foreground">{f.label}</dt>
                  <dd className="break-words">{f.value}</dd>
                </div>
              ))}
            </dl>
          )}
          <p className="whitespace-pre-wrap break-words">{item.text}</p>
        </div>
        <fieldset className="space-y-2 text-sm">
          <legend className="sr-only">Their email address</legend>
          {item.candidates.map((c) => (
            <label key={c} className="flex items-center gap-2 break-all">
              <input
                type="radio"
                name="feed-person"
                checked={picked === c}
                onChange={() => setPicked(c)}
              />
              {c}
            </label>
          ))}
          <label className="flex flex-wrap items-center gap-2">
            <input
              type="radio"
              name="feed-person"
              checked={picked === ""}
              onChange={() => setPicked("")}
            />
            Another address
            <input
              type="email"
              aria-label="Another email address"
              className={`${field} min-w-56 flex-1`}
              placeholder="name@company.example"
              value={other}
              maxLength={320}
              onFocus={() => setPicked("")}
              onChange={(e) => {
                setPicked("");
                setOther(e.target.value);
              }}
            />
          </label>
        </fieldset>
        <ErrorText>{error}</ErrorText>
        <DialogFooter>
          <Button variant="ghost" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button disabled={busy || !email} onClick={() => void submit()}>
            {busy ? "Saving…" : "This is the person"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
