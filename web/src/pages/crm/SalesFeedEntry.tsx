import { useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { ExternalLink } from "lucide-react";
import { CopyButton } from "@/ui";
import { track } from "@/lib/analytics";
import { salesExternalURL } from "@/lib/sales";
import { salesFeedKinds } from "./SalesFeedAdd";

type Item = Schemas["SalesFeedItem"];
type Kind = Schemas["SalesFeedKind"];

const textLimit = 600; // characters of a post shown before "Show more"
const repliesShown = 3; // replies shown before they fold behind "N replies"

const when = (v: string) => new Date(v).toLocaleString();

export const salesFeedKindLabel = (kind: Kind) =>
  salesFeedKinds.find(([k]) => k === kind)?.[1] ?? kind;

// "New person · #channel", or just the kind when the channel is gone.
export function salesFeedSource(kind: Kind, channelName: string) {
  const label = salesFeedKindLabel(kind);
  return channelName ? `${label} · #${channelName}` : label;
}

// The conversation's email messages and Slack posts in one list, newest
// first. Each list keeps its own order; they are merged by time.
export function salesTimeline<M extends { id?: string; occurred_at: string }>(
  messages: M[],
  items: Item[],
) {
  type Entry =
    | { key: string; message: M; item?: undefined }
    | { key: string; item: Item; message?: undefined };
  const at = (v: string) => Date.parse(v) || 0;
  const out: Entry[] = [];
  let i = 0;
  let j = 0;
  while (i < messages.length || j < items.length) {
    if (
      j >= items.length ||
      (i < messages.length &&
        at(messages[i].occurred_at) <= at(items[j].occurred_at))
    ) {
      out.push({ key: `message:${messages[i].id ?? i}`, message: messages[i] });
      i++;
    } else {
      out.push({ key: `feed:${items[j].id}`, item: items[j] });
      j++;
    }
  }
  return out.reverse();
}

// The colours a source's badge can take. A source keeps its colour: it is
// picked from the name, so the same tool looks the same in every row.
const sourceTints = [
  "border-sky-600/25 bg-sky-500/10 text-sky-700 dark:border-sky-400/25 dark:text-sky-300",
  "border-violet-600/25 bg-violet-500/10 text-violet-700 dark:border-violet-400/25 dark:text-violet-300",
  "border-emerald-600/25 bg-emerald-500/10 text-emerald-700 dark:border-emerald-400/25 dark:text-emerald-300",
  "border-amber-600/30 bg-amber-500/10 text-amber-700 dark:border-amber-400/25 dark:text-amber-300",
  "border-pink-600/25 bg-pink-500/10 text-pink-700 dark:border-pink-400/25 dark:text-pink-300",
  "border-teal-600/25 bg-teal-500/10 text-teal-700 dark:border-teal-400/25 dark:text-teal-300",
];
const replyTint = "border-border bg-muted text-muted-foreground";
// A post a person typed is named by its channel, without a colour of its own.
const channelTint = "border-dashed border-border text-foreground";

function tintOf(name: string) {
  let h = 0;
  for (const c of name.toLowerCase()) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  return sourceTints[h % sourceTints.length];
}

// Where a conversation or post came from, as one badge: the tool that posted
// it ("Fillout"), or the channel when a person posted it, or "Email reply"
// for a conversation that started from a reply.
export function salesSourceName(
  source: string | null | undefined,
  kind: Kind | null | undefined,
  channelName: string,
) {
  if (source) return source;
  if (!kind) return "Email reply";
  return channelName ? `#${channelName}` : salesFeedKindLabel(kind);
}

export function SalesSourceBadge({
  source,
  kind,
  channelName,
}: {
  source: string | null | undefined;
  kind: Kind | null | undefined;
  channelName: string;
}) {
  const name = salesSourceName(source, kind, channelName);
  return (
    <span
      className={`inline-flex h-5 max-w-[14rem] items-center rounded border px-1.5 text-[11px] font-medium ${source ? tintOf(name) : kind ? channelTint : replyTint}`}
      title={kind ? salesFeedSource(kind, channelName) : undefined}
    >
      <span className="truncate">{name}</span>
    </span>
  );
}

// A tool's field name as a label: "default_title" reads "Title",
// "company_size" reads "Company size".
export function salesFieldLabel(label: string) {
  const words = label
    .replace(/^default[_\s]+/i, "")
    .replace(/_+/g, " ")
    .trim();
  return words ? words[0].toUpperCase() + words.slice(1) : label;
}

const fieldKey = (v: string) =>
  v
    .replace(/^[\s•·–—\-*>]+/, "")
    .replace(/[*_]/g, "")
    .trim()
    .toLowerCase();

// The lines of a post that are not one of its "Label: value" fields: what is
// left to read once the fields are shown as a table.
export function salesPostRest(text: string, fields: Item["fields"]) {
  const labels = new Set(fields.map((f) => fieldKey(f.label)));
  return text
    .split("\n")
    .filter((line) => {
      const i = line.indexOf(":");
      return !(i > 0 && labels.has(fieldKey(line.slice(0, i))));
    })
    .join("\n")
    .trim();
}

const emailValue = /^[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+$/;

// A field's value: an address can be copied and written to, a link opens,
// anything else is text. Everything came from Slack and is never markup.
function FieldValue({ value }: { value: string }) {
  const v = value.trim();
  const link = /^https?:\/\//i.test(v) ? salesExternalURL(v) : undefined;
  if (emailValue.test(v))
    return (
      <span className="inline-flex items-center gap-0.5">
        <a href={`mailto:${v}`} className="break-all hover:underline">
          {v}
        </a>
        <CopyButton text={v} label="Copy email address" />
      </span>
    );
  if (link)
    return (
      <a
        href={link}
        target="_blank"
        rel="noreferrer noopener"
        className="inline-flex items-center gap-1 break-all hover:underline"
      >
        {v} <ExternalLink className="size-3 shrink-0" />
      </a>
    );
  return <span className="whitespace-pre-wrap break-words">{value}</span>;
}

// Text from Slack, shown as text: the first 600 characters, then "Show more".
function PostText({ text, className }: { text: string; className: string }) {
  const [more, setMore] = useState(false);
  const chars = Array.from(text);
  const long = chars.length > textLimit;
  return (
    <div className={className}>
      <p className="whitespace-pre-wrap break-words">
        {long && !more ? `${chars.slice(0, textLimit).join("")}…` : text}
      </p>
      {long && (
        <button
          type="button"
          className="mt-1 text-xs underline underline-offset-2"
          onClick={() => setMore(!more)}
        >
          {more ? "Show less" : "Show more"}
        </button>
      )}
    </div>
  );
}

// One Slack post on the conversation page, laid out for reading rather than
// as Slack shows it: where it came from, what the person wrote, then the
// post's details as a table. The post as written folds away underneath.
// Everything in it came from Slack and is shown as text; the only links pass
// salesExternalURL. A post on no conversation (lead_id null) is there as
// context about the person or their company, and says so.
export function SalesFeedEntry({ item }: { item: Item }) {
  const [allReplies, setAllReplies] = useState(false);
  const link = salesExternalURL(item.link);
  const folded = item.replies.length > repliesShown && !allReplies;
  // What they wrote: the AI's reading when there is one, else the lines that
  // are not fields.
  const rest = item.message ? "" : salesPostRest(item.text, item.fields);
  // The fields the channel shows first lead; the rest follow what they wrote.
  // Their values are cut for the queue, so the full ones come from the post.
  const keyFields = (item.key_fields ?? []).map((k) => ({
    label: k.label,
    value:
      item.fields.find((f) => fieldKey(f.label) === fieldKey(k.label))
        ?.value ?? k.value,
  }));
  const otherFields = item.fields.filter(
    (f) => !keyFields.some((k) => fieldKey(k.label) === fieldKey(f.label)),
  );
  // What they wrote is not repeated when it is already one of those fields.
  const flat = (v: string) => v.replace(/\s+/g, " ").trim();
  const said = item.message || rest;
  // Nor when it only says the fields over again ("Traffic: 1TB+. ...").
  const restates = item.fields.some((f) =>
    flat(said).toLowerCase().includes(`${fieldKey(f.label)}:`),
  );
  const body =
    restates ||
    keyFields.some((k) => k.value && flat(k.value).includes(flat(said)))
      ? ""
      : said;
  // The post as written adds something only when it was rearranged above.
  const original =
    item.text && (item.message || item.fields.length > 0) ? item.text : "";
  return (
    <div className="rounded-xl border bg-card p-5">
      <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
        <span className="flex flex-wrap items-center gap-2">
          <SalesSourceBadge
            source={item.source}
            kind={item.kind}
            channelName={item.channel_name}
          />
          <span>{salesFeedSource(item.kind, item.channel_name)}</span>
        </span>
        <span className="flex flex-wrap items-center gap-2">
          {!item.lead_id && (
            <span className="rounded border px-1.5 py-0.5">
              On their record
            </span>
          )}
          {item.deleted_in_slack && (
            <span className="rounded border px-1.5 py-0.5">
              Deleted in Slack
            </span>
          )}
          {item.edited_at && (
            <span
              className="rounded border px-1.5 py-0.5"
              title={when(item.edited_at)}
            >
              Edited
            </span>
          )}
          <time dateTime={item.occurred_at}>{when(item.occurred_at)}</time>
        </span>
      </div>
      {keyFields.length > 0 && (
        <dl className="mt-4 grid gap-x-4 gap-y-2 text-sm sm:grid-cols-[minmax(8rem,14rem)_1fr]">
          {keyFields.map((f, i) => (
            <div key={i} className="contents">
              <dt className="text-xs text-muted-foreground sm:pt-0.5">
                {salesFieldLabel(f.label)}
              </dt>
              <dd className="min-w-0 font-medium">
                {f.value ? (
                  <FieldValue value={f.value} />
                ) : (
                  <span className="font-normal text-muted-foreground">
                    Not given
                  </span>
                )}
              </dd>
            </div>
          ))}
        </dl>
      )}
      {/* A form or ticket needs no sentence saying what it is. */}
      {item.summary && item.fields.length === 0 && (
        <p className="mt-3 text-sm font-medium break-words">{item.summary}</p>
      )}
      {body && (
        <PostText
          text={body}
          className="mt-3 border-l-2 pl-3 text-sm leading-relaxed"
        />
      )}
      {otherFields.length > 0 && (
        <dl
          className={`grid gap-x-4 gap-y-2 text-sm sm:grid-cols-[minmax(8rem,14rem)_1fr] ${keyFields.length > 0 ? "mt-4 border-t pt-4" : "mt-4"}`}
        >
          {otherFields.map((f, i) => (
            <div key={i} className="contents">
              <dt className="text-xs text-muted-foreground sm:pt-0.5">
                {salesFieldLabel(f.label)}
              </dt>
              <dd className="min-w-0">
                <FieldValue value={f.value} />
              </dd>
            </div>
          ))}
        </dl>
      )}
      {item.replies.length > 0 && (
        <div className="mt-4 border-l pl-3 text-xs">
          {folded ? (
            <button
              type="button"
              className="text-muted-foreground underline underline-offset-2"
              onClick={() => setAllReplies(true)}
            >
              {item.replies.length} replies in Slack
            </button>
          ) : (
            <ol className="space-y-2">
              {item.replies.map((r, i) => (
                <li key={i}>
                  <time className="block text-muted-foreground" dateTime={r.at}>
                    {when(r.at)}
                  </time>
                  <p className="whitespace-pre-wrap break-words">{r.text}</p>
                </li>
              ))}
            </ol>
          )}
        </div>
      )}
      <div className="mt-4 flex flex-wrap items-start gap-x-4 gap-y-2 text-xs text-muted-foreground">
        {original && (
          <details className="min-w-0 flex-1 basis-48">
            <summary className="cursor-pointer">The post as written</summary>
            <PostText text={original} className="mt-2" />
          </details>
        )}
        {link && (
          <a
            href={link}
            target="_blank"
            rel="noreferrer noopener"
            className="inline-flex items-center gap-1 hover:text-foreground hover:underline"
          >
            Open in Slack <ExternalLink className="size-3" />
          </a>
        )}
      </div>
    </div>
  );
}

// The outside CRM's page for the person, from the newest post that has one
// link: "Open in app.example.com". Only http(s) links are shown.
export function SalesCrmLink({ items }: { items: Item[] }) {
  const newest = [...items].reverse().find((i) => salesExternalURL(i.crm_link));
  const href = salesExternalURL(newest?.crm_link);
  if (!newest || !href) return null;
  return (
    <a
      href={href}
      target="_blank"
      rel="noreferrer noopener"
      onClick={() => track("ui.sales_crm_link_opened", { kind: newest.kind })}
      className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground hover:underline"
    >
      Open in {new URL(href).host} <ExternalLink className="size-3" />
    </a>
  );
}

// A conversation from Slack has no email thread yet, so nothing on it sends
// the first email: the rep copies the address instead.
export function SalesSlackArrival({ email }: { email: string }) {
  return (
    <p className="flex flex-wrap items-center gap-x-2 gap-y-1">
      <span>
        Arrived through Slack. Writing the first email from here is coming;
        until then, copy the address.
      </span>
      {email && (
        <span className="inline-flex items-center gap-0.5 text-foreground">
          <span className="break-all">{email}</span>
          <CopyButton text={email} label="Copy email address" />
        </span>
      )}
    </p>
  );
}
