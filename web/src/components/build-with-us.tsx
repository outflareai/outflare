import { useEffect, useRef, useState, type ReactNode } from "react";
import releases, { type Release } from "virtual:outflare-changelog";
import { api } from "../api";
import { Icon, type IconName } from "../icons";
import { Button } from "../ui";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { track } from "@/lib/analytics";
import { cn } from "@/lib/utils";
import { marks } from "@/components/community-links";

// Feedback and What's new, in the top bar on every page. Outflare is built
// in the open: feedback goes straight to GitHub, posted by the person under
// their own account, as an issue (a bug) or a discussion (an idea or a
// question), then, if they like, on X so others can back it. Anyone can take
// it from there to a pull request. Nothing is sent from this server: the box
// only opens GitHub or X with the note filled in. Both say how fast Outflare moves, from the
// changelog this build carries.

const repo = "https://github.com/outflareai/outflare";
export const github = {
  discussions: `${repo}/discussions`,
  issues: `${repo}/issues`,
  contribute: `${repo}/blob/main/CONTRIBUTING.md`,
  goodFirst: `${repo}/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22`,
  codespace: "https://codespaces.new/outflareai/outflare",
  changelog: "https://outflare.ai/changelog/",
};
const support = "mailto:hello@outflare.ai";

// Three kinds, each with its home on GitHub: a bug is an issue the team
// can act on; an idea (a feature request is an idea) and a question are
// discussions, where people vote and answers stay findable.
type Kind = "bug" | "idea" | "question";
const kinds: { id: Kind; label: string; placeholder: string; github: string; where: string; x: string }[] = [
  { id: "bug", label: "Bug", placeholder: "What happened, and what did you expect? The first line becomes the title.", github: "Open an issue on GitHub", where: "an issue the team triages every day", x: "found a bug in" },
  { id: "idea", label: "Idea", placeholder: "What should Outflare do? The first line becomes the title.", github: "Post the idea on GitHub", where: "a discussion in Ideas, where people vote", x: "idea for" },
  { id: "question", label: "Question", placeholder: "What do you want to know? The first line becomes the title.", github: "Ask on GitHub", where: "a discussion in Q&A, where the answer helps the next person", x: "question about" },
];
type Channel = "github" | "x";

// Releases in the last 7 days of the changelog this build carries.
const DAY = 86_400_000;
const today = () => new Date().toISOString().slice(0, 10);
function releasesSince(days: number): Release[] {
  const from = new Date(Date.now() - days * DAY).toISOString().slice(0, 10);
  return releases.filter((r) => r.date >= from);
}
function pace(): string {
  const week = releasesSince(7);
  if (week.length === 0) return "Outflare is built in the open, on GitHub.";
  const changes = week.reduce((n, r) => n + r.changes.length, 0);
  return `${week.length} ${week.length === 1 ? "release" : "releases"} and ${changes} ${changes === 1 ? "change" : "changes"} in the last 7 days, built in the open on GitHub.`;
}

// The note, filled in, on GitHub (a new issue for a bug, a discussion in
// the matching category otherwise) or on X. On X it speaks to the builder,
// a person, and tags Outflare too: people answer people, and the builder's
// reply is what spreads it. Nothing is posted until the person posts it.
function xPrefix(kind: Kind, builder: string): string {
  const what = kinds.find((k) => k.id === kind)!.x;
  return builder ? `Hey @${builder}, ${what} @outflareai:` : `${what[0].toUpperCase()}${what.slice(1)} @outflareai:`;
}
function draftURL(channel: Channel, kind: Kind, message: string, page: string, version: string, builder: string): string {
  if (channel === "x") {
    const prefix = xPrefix(kind, builder);
    return `https://x.com/intent/post?${new URLSearchParams({ text: `${prefix} ${message.slice(0, 280 - prefix.length - 1)}` })}`;
  }
  const [first, ...rest] = message.split("\n");
  const title = first.slice(0, 100);
  const detail = (first.length > 100 ? message : rest.join("\n")).trim();
  const body = `${detail}\n\n---\nPage: ${page || "unknown"} · Outflare ${version || "unknown"}`.trimStart();
  const q = new URLSearchParams({ title, body });
  if (kind === "bug") return `${github.issues}/new?${q}`;
  q.set("category", kind === "question" ? "q-a" : "ideas");
  return `${github.discussions}/new?${q}`;
}

let version: Promise<string> | undefined;
function useVersion(on: boolean): string {
  const [v, setV] = useState("");
  useEffect(() => {
    if (!on) return;
    version ??= api.GET("/v1/meta").then(({ data }) => (data as { version?: string } | undefined)?.version ?? "").catch(() => "");
    version.then(setV);
  }, [on]);
  return v;
}

// The builder's X handle, from the server's "Follow the builder" link;
// empty when the server has none.
let builderHandle: Promise<string> | undefined;
function useBuilder(on: boolean): string {
  const [h, setH] = useState("");
  useEffect(() => {
    if (!on) return;
    builderHandle ??= api
      .GET("/v1/me/community")
      .then(({ data }) => {
        const url = data?.links.find((l) => l.id === "builder")?.url ?? "";
        return /^https:\/\/(?:x|twitter)\.com\/([A-Za-z0-9_]{1,15})\/?$/.exec(url)?.[1] ?? "";
      })
      .catch(() => "");
    builderHandle.then(setH);
  }, [on]);
  return h;
}

function out(target: string, from: string, kind?: Kind) {
  track("ui.community_link_opened", { target, from, kind });
}

// Anywhere in the app can open the Feedback box: What's new, the command
// palette, an empty state.
export const openFeedback = () => window.dispatchEvent(new CustomEvent("outflare:feedback"));

export function FeedbackButton({ page }: { page: string }) {
  const [open, setOpen] = useState(false);
  const [kind, setKind] = useState<Kind>("idea");
  const [message, setMessage] = useState("");
  const [posted, setPosted] = useState<Record<Channel, boolean> | null>(null);
  const v = useVersion(open);
  const builder = useBuilder(open);
  const box = useRef<HTMLTextAreaElement>(null);
  const k = kinds.find((x) => x.id === kind)!;

  useEffect(() => {
    const on = () => {
      setOpen(true);
      track("ui.feedback_opened", { from: "event" });
    };
    window.addEventListener("outflare:feedback", on);
    return () => window.removeEventListener("outflare:feedback", on);
  }, []);

  function toggle(o: boolean) {
    setOpen(o);
    if (o) track("ui.feedback_opened", { from: "top_bar" });
    else if (posted) {
      setPosted(null);
      setMessage("");
    }
  }

  function post(channel: Channel) {
    const text = message.trim();
    if (!text) return;
    out(channel === "x" ? "x_post" : kind === "bug" ? "issue_draft" : "discussion_draft", posted ? "feedback_step2" : "feedback", kind);
    window.open(draftURL(channel, kind, text, page, v, builder), "_blank", "noopener,noreferrer");
    setPosted((p) => ({ github: false, x: false, ...p, [channel]: true }));
  }

  return (
    <Popover open={open} onOpenChange={toggle}>
      <PopoverTrigger asChild>
        <button className="inline-flex h-7 items-center gap-1.5 rounded-md border px-2 text-[12px] text-muted-foreground shadow-xs transition-colors hover:bg-accent hover:text-foreground data-[state=open]:bg-accent data-[state=open]:text-foreground">
          <Icon name="feedback" className="size-3.5" />
          <span className="hidden sm:inline">Feedback</span>
          <span className="sr-only sm:hidden">Feedback</span>
        </button>
      </PopoverTrigger>
      <PopoverContent align="end" collisionPadding={12} className="w-[min(24rem,calc(100vw-1.5rem))] p-0" onOpenAutoFocus={(e) => { e.preventDefault(); box.current?.focus(); }}>
        {posted ? (
          <div className="space-y-3 p-4" role="status">
            <p className="font-medium">Almost there: post it in the new tab.</p>
            <ol className="space-y-2">
              <Step n={1} done={posted.github} label={posted.github ? "Filled in on GitHub" : "Track it on GitHub, where it gets built"}>
                <Button variant="ghost" className="border" onClick={() => post("github")}>{marks.github}GitHub</Button>
              </Step>
              <Step n={2} done={posted.x} label={posted.x ? "Filled in on X" : builder ? `Tell @${builder} on X, so people can back it` : "Tell people on X, so they can back it"}>
                <Button variant="ghost" className="border" onClick={() => post("x")}>{marks.x}Post on X</Button>
              </Step>
            </ol>
            <div className="flex justify-end gap-2">
              <Button variant="ghost" onClick={() => { setPosted(null); setMessage(""); box.current?.focus(); }}>Post another</Button>
              <Button onClick={() => toggle(false)}>Done</Button>
            </div>
          </div>
        ) : (
          <form className="space-y-3 p-4" onSubmit={(e) => { e.preventDefault(); post("github"); }}>
            <div>
              <p className="font-medium">Shape what we build next</p>
              <p className="mt-0.5 text-[12px] text-muted-foreground">{pace()}</p>
            </div>
            <div role="radiogroup" aria-label="What kind of feedback" className="flex flex-wrap gap-1.5">
              {kinds.map((x) => (
                <button
                  key={x.id}
                  type="button"
                  role="radio"
                  aria-checked={kind === x.id}
                  onClick={() => { setKind(x.id); box.current?.focus(); }}
                  className={cn("rounded-full border px-2.5 py-0.5 text-[12px] hover:bg-accent", kind === x.id ? "border-primary bg-primary/10 text-foreground" : "text-muted-foreground hover:text-foreground")}
                >
                  {x.label}
                </button>
              ))}
            </div>
            <textarea
              ref={box}
              value={message}
              onChange={(e) => setMessage(e.target.value)}
              onKeyDown={(e) => { if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) { e.preventDefault(); post("github"); } }}
              rows={4}
              aria-label="Your feedback"
              placeholder={k.placeholder}
              className="w-full resize-y rounded-md border border-input bg-transparent px-2.5 py-2 text-[13px] shadow-xs outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/40 dark:bg-input/30"
            />
            <div className="grid gap-2">
              <Button type="submit" disabled={!message.trim()} className="w-full justify-center">
                {marks.github}
                {k.github}
              </Button>
              <Button type="button" variant="ghost" disabled={!message.trim()} onClick={() => post("x")} className="w-full justify-center border">
                {marks.x}
                Post on X
              </Button>
            </div>
            <p className="text-[12px] text-muted-foreground">
              On GitHub it becomes {k.where}{v && `, with the page and version (${v})`}.{builder && ` On X it goes to @${builder}, who builds Outflare in public.`} Both are public and under your own account: leave out your customers' data and any keys.
            </p>
          </form>
        )}
        <div className="border-t px-2 py-2">
          <p className="px-2 pb-1 font-mono text-[10px] tracking-wider text-muted-foreground uppercase">Build it with us</p>
          <OutLink href={github.goodFirst} icon="pull-request" target="good_first_issue" from="feedback">Pick a good first issue</OutLink>
          <OutLink href={github.codespace} icon="code" target="codespace" from="feedback">Open the code in your browser</OutLink>
          <OutLink href={github.discussions} icon="discuss" target="discussions" from="feedback">Browse ideas and vote</OutLink>
        </div>
        <p className="border-t px-4 py-2.5 text-[12px] text-muted-foreground">
          Need help with your own account or data?{" "}
          <a href={support} onClick={() => out("support_email", "feedback")} className="underline underline-offset-2 hover:text-foreground">Email us privately</a>
        </p>
      </PopoverContent>
    </Popover>
  );
}

function Step({ n, done, label, children }: { n: number; done: boolean; label: string; children: ReactNode }) {
  return (
    <li className="flex items-center gap-3 rounded-md border p-2.5">
      <span aria-hidden="true" className={cn("flex size-6 shrink-0 items-center justify-center rounded-full border font-mono text-[11px]", done && "border-transparent bg-green-500/15 text-green-600 dark:text-green-400")}>
        {done ? <Icon name="check" className="size-3" /> : n}
      </span>
      <span className="min-w-0 flex-1 text-[13px]">{done && <span className="sr-only">Done: </span>}{label}</span>
      {!done && children}
    </li>
  );
}

function OutLink({ href, icon, target, from, children }: { href: string; icon: IconName; target: string; from: string; children: ReactNode }) {
  return (
    <a href={href} target="_blank" rel="noopener noreferrer" onClick={() => out(target, from)} className="group flex items-center gap-2.5 rounded-md px-2 py-1.5 text-[13px] hover:bg-accent">
      <Icon name={icon} className="size-3.5 text-muted-foreground group-hover:text-foreground" />
      <span className="flex-1">{children}</span>
      <Icon name="external" className="size-3 text-muted-foreground/70 group-hover:text-foreground" />
    </a>
  );
}

// ---- What's new ----

const seenKey = "outflare:whats-new-seen";
function readSeen(): string | null {
  try {
    return localStorage.getItem(seenKey);
  } catch {
    return null;
  }
}
// How many releases are newer than the one the person last saw. Someone
// who has never opened it sees the last week's.
function unseenCount(seen: string | null): number {
  if (releases.length === 0) return 0;
  if (seen === null) return releasesSince(7).length;
  const i = releases.findIndex((r) => r.version === seen);
  return i === -1 ? releases.length : i;
}

const kindLabel: Record<string, { label: string; tone: string }> = {
  added: { label: "New", tone: "bg-green-500/15 text-green-700 dark:text-green-300" },
  changed: { label: "Improved", tone: "bg-sky-500/15 text-sky-700 dark:text-sky-300" },
  fixed: { label: "Fixed", tone: "bg-amber-500/15 text-amber-800 dark:text-amber-300" },
  security: { label: "Security", tone: "bg-red-500/15 text-red-700 dark:text-red-300" },
};

function dayLabel(date: string): string {
  if (date === today()) return "Today";
  if (date === new Date(Date.now() - DAY).toISOString().slice(0, 10)) return "Yesterday";
  return new Date(`${date}T12:00:00Z`).toLocaleDateString(undefined, { weekday: "short", month: "short", day: "numeric", timeZone: "UTC" });
}

// Inline Markdown from the changelog: `code`, **bold** and [links](url).
function inline(text: string): ReactNode[] {
  const parts: ReactNode[] = [];
  const re = /`([^`]+)`|\*\*([^*]+)\*\*|\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)/g;
  let last = 0;
  for (let m = re.exec(text); m; m = re.exec(text)) {
    if (m.index > last) parts.push(text.slice(last, m.index));
    if (m[1]) parts.push(<code key={m.index} className="rounded bg-muted px-1 font-mono text-[12px]">{m[1]}</code>);
    else if (m[2]) parts.push(<strong key={m.index} className="font-medium">{m[2]}</strong>);
    else parts.push(<a key={m.index} href={m[4]} target="_blank" rel="noopener noreferrer" className="underline underline-offset-2">{m[3]}</a>);
    last = re.lastIndex;
  }
  if (last < text.length) parts.push(text.slice(last));
  return parts;
}

export function WhatsNewButton() {
  const [open, setOpen] = useState(false);
  const [unseen, setUnseen] = useState(() => unseenCount(readSeen()));
  if (releases.length === 0) return null;

  function show(o: boolean) {
    setOpen(o);
    if (!o) return;
    track("ui.whats_new_opened", { unseen });
    try {
      localStorage.setItem(seenKey, releases[0].version);
    } catch {
      // Private window: the dot comes back next time, which is fine.
    }
    setUnseen(0);
  }

  const days = releases.reduce<{ date: string; releases: Release[] }[]>((acc, r) => {
    const d = acc.at(-1);
    if (d?.date === r.date) d.releases.push(r);
    else acc.push({ date: r.date, releases: [r] });
    return acc;
  }, []);

  return (
    <>
      <button
        onClick={() => show(true)}
        aria-label={unseen > 0 ? `What's new: ${unseen} new ${unseen === 1 ? "release" : "releases"}` : "What's new"}
        className="relative inline-flex h-7 items-center gap-1.5 rounded-md border px-2 text-[12px] text-muted-foreground shadow-xs transition-colors hover:bg-accent hover:text-foreground"
      >
        <Icon name="sparkles" className="size-3.5" />
        <span className="hidden lg:inline">What's new</span>
        {unseen > 0 && <span aria-hidden="true" className="absolute -top-1 -right-1 size-2.5 rounded-full bg-brand-strong ring-2 ring-background dark:bg-brand" />}
      </button>
      <Sheet open={open} onOpenChange={show}>
        <SheetContent className="w-full gap-0 sm:max-w-md">
          <SheetHeader className="border-b">
            <SheetTitle>What's new</SheetTitle>
            <SheetDescription>{pace().replace(" Your note can be the next one.", "")} Built in the open, with ideas from people like you.</SheetDescription>
          </SheetHeader>
          <div className="flex-1 overflow-y-auto px-4 py-2">
            {days.map((d) => (
              <section key={d.date} aria-labelledby={`day-${d.date}`} className="py-3">
                <h3 id={`day-${d.date}`} className="sticky top-0 z-10 -mx-1 flex items-baseline gap-2 bg-background px-1 py-1 text-[13px] font-medium">
                  {dayLabel(d.date)}
                  <span className="font-mono text-[11px] font-normal text-muted-foreground">{d.releases.length} {d.releases.length === 1 ? "release" : "releases"}</span>
                </h3>
                <ul className="mt-1 space-y-2.5">
                  {d.releases.flatMap((r) =>
                    r.changes.map((c, i) => (
                      <li key={`${r.version}-${i}`} className="text-[13px] leading-relaxed">
                        <span className={cn("mr-1.5 inline-block rounded px-1.5 py-px align-[1px] text-[11px] font-medium", (kindLabel[c.kind] ?? kindLabel.changed).tone)}>{(kindLabel[c.kind] ?? kindLabel.changed).label}</span>
                        {inline(c.text)}
                        <span className="ml-1.5 font-mono text-[11px] text-muted-foreground/70">{r.version}</span>
                      </li>
                    )),
                  )}
                </ul>
              </section>
            ))}
          </div>
          <SheetFooter className="border-t">
            <p className="text-[13px] font-medium">What should we build next?</p>
            <div className="flex flex-wrap gap-2">
              <Button onClick={() => { show(false); openFeedback(); }}>
                <Icon name="feedback" className="size-3.5" />
                Send feedback
              </Button>
              <a href={github.goodFirst} target="_blank" rel="noopener noreferrer" onClick={() => out("good_first_issue", "whats_new")} className="inline-flex h-8 items-center gap-1.5 rounded-md border px-3 text-[13px] shadow-xs hover:bg-accent">
                <Icon name="pull-request" className="size-3.5" />
                Build it with us
              </a>
            </div>
            <a href={github.changelog} target="_blank" rel="noopener noreferrer" onClick={() => out("changelog", "whats_new")} className="text-[12px] text-muted-foreground underline underline-offset-2 hover:text-foreground">
              Every change, day by day, on outflare.ai/changelog
            </a>
          </SheetFooter>
        </SheetContent>
      </Sheet>
    </>
  );
}
