import { useEffect, useState, type ReactNode } from "react";
import type { components } from "@outflare/sdk";
import { api } from "../api";
import { Icon } from "../icons";
import { cn } from "@/lib/utils";

type Community = components["schemas"]["CommunityLinks"];
type Link = Community["links"][number];
type HeardFrom = components["schemas"]["HeardFrom"];

// The answers to "How did you hear about Outflare?", in the order shown.
const heardFrom: [HeardFrom, string][] = [
  ["search", "Search"],
  ["ai_assistant", "An AI assistant"],
  ["x", "X"],
  ["linkedin", "LinkedIn"],
  ["github", "GitHub"],
  ["youtube", "YouTube"],
  ["friend", "A friend or colleague"],
  ["newsletter_podcast", "A newsletter or podcast"],
  ["other", "Somewhere else"],
];

// "Star us on GitHub", "Follow us on X" (releases) and "Follow the builder"
// (Outflare built in public, on its builder's own X): optional, one click each. Nobody
// can check a star or a follow, so opening the link ticks the item. Each
// person has their own, and the server's operator can turn them off.
// step: the Get started step, which shows the links even once they're all
// done, asks how the person heard about Outflare (optional, one tap), and
// calls onClicked after each link is saved.
export function CommunityLinks({ className, step = false, onClicked }: { className?: string; step?: boolean; onClicked?: () => void }) {
  const [c, setC] = useState<Community | null>(null);
  useEffect(() => {
    api.GET("/v1/me/community").then(({ data }) => data && setC(data));
  }, []);
  if (!c || (step ? c.links.length === 0 : !c.show)) return null;

  async function patch(body: { clicked?: string; hidden?: boolean; heard_from?: HeardFrom }) {
    const { data } = await api.PATCH("/v1/me/community", { body });
    if (data) setC(body.hidden ? { ...data, show: false } : data);
    if (data && body.clicked) onClicked?.();
  }

  function opened(l: Link) {
    // Ticked straight away; the card stays until the person leaves the page.
    setC((cur) => cur && { ...cur, links: cur.links.map((x) => (x.id === l.id ? { ...x, done: true } : x)) });
    if (!l.done) patch({ clicked: l.id });
  }

  return (
    <section aria-labelledby={step ? undefined : "community-links"} aria-label={step ? "Star and follow" : undefined} className={cn("rounded-lg border bg-card shadow-xs", className)}>
      {!step && <div className="flex items-center gap-3 border-b px-4 py-3">
        <h2 id="community-links" className="flex-1 font-medium">Help Outflare grow</h2>
        <button onClick={() => patch({ hidden: true })} className="-m-1 rounded-md p-2 text-[12px] text-muted-foreground hover:bg-accent hover:text-foreground">
          Hide
        </button>
      </div>}
      <ul className="divide-y">
        {c.links.map((l) => (
          <li key={l.id}>
            <a href={l.url} target="_blank" rel="noopener noreferrer" onClick={() => opened(l)} className={cn("group flex items-center gap-3 px-4 hover:bg-accent/50", step ? "py-3.5" : "py-2.5")}>
              <span
                aria-hidden="true"
                className={cn("flex size-5 shrink-0 items-center justify-center rounded-full border", l.done ? "border-transparent bg-green-500/15 text-green-600 dark:text-green-400" : "border-input")}
              >
                {l.done && <Icon name="check" className="size-3" />}
              </span>
              <span className="sr-only">{l.done ? "Done: " : ""}</span>
              <span className="flex size-4 shrink-0 items-center justify-center text-muted-foreground group-hover:text-foreground">{marks[l.id]}</span>
              <span className={cn("min-w-0 flex-1 text-[14px]", l.done && "text-muted-foreground")}>{l.title}</span>
              <Icon name="external" className="size-3.5 shrink-0 text-muted-foreground/70 group-hover:text-foreground" />
            </a>
          </li>
        ))}
      </ul>
      {step && (
        <div role="group" aria-labelledby="heard-from" className="border-t px-4 py-3.5">
          <p id="heard-from" className="text-[13px] text-muted-foreground">
            How did you hear about Outflare? <span className="text-muted-foreground/70">Optional</span>
          </p>
          <div className="mt-2.5 flex flex-wrap gap-1.5">
            {heardFrom.map(([id, label]) => (
              <button
                key={id}
                type="button"
                aria-pressed={c.heard_from === id}
                onClick={() => {
                  setC((cur) => cur && { ...cur, heard_from: id });
                  patch({ heard_from: id });
                }}
                className={cn(
                  "rounded-full border px-3 py-1 text-[13px] hover:bg-accent",
                  c.heard_from === id ? "border-primary bg-primary/10 text-foreground" : "text-muted-foreground hover:text-foreground",
                )}
              >
                {label}
              </button>
            ))}
          </div>
          {c.heard_from && <p className="mt-2 text-[12px] text-muted-foreground">Thanks, that helps us know where to show up.</p>}
        </div>
      )}
    </section>
  );
}

export const marks: Record<Link["id"], ReactNode> = {
  github: (
    <svg viewBox="0 0 16 16" className="size-4" aria-hidden="true">
      <path fill="currentColor" d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0016 8c0-4.42-3.58-8-8-8z" />
    </svg>
  ),
  x: (
    <svg viewBox="0 0 24 24" className="size-3.5" aria-hidden="true">
      <path fill="currentColor" d="M18.244 2.25h3.308l-7.227 8.26 8.502 11.24H16.17l-5.214-6.817L4.99 21.75H1.68l7.73-8.835L1.254 2.25H8.08l4.713 6.231zm-1.161 17.52h1.833L7.084 4.126H5.117z" />
    </svg>
  ),
  builder: (
    <svg viewBox="0 0 24 24" className="size-3.5" aria-hidden="true">
      <path fill="currentColor" d="M18.244 2.25h3.308l-7.227 8.26 8.502 11.24H16.17l-5.214-6.817L4.99 21.75H1.68l7.73-8.835L1.254 2.25H8.08l4.713 6.231zm-1.161 17.52h1.833L7.084 4.126H5.117z" />
    </svg>
  ),
};
