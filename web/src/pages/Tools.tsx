import type { Tab } from "../App";
import { Icon, type IconName } from "../icons";
import { PageHeader } from "../ui";
import { track } from "@/lib/analytics";
import { everyday, modules, shortcuts, togglePin, useNavPrefs, tabBlurbs, tabDocs, tabNames, type NavGroup } from "../shell";

const docsHost = "https://outflare.ai";

// Every tool in the app, grouped by module as the sidebar is, with what each one is
// for and a link to its guide: the place to learn what Outflare can do.
const groups: NavGroup[] = [
  { label: "Run it from your AI", tabs: ["ai", "company"] },
  { label: "Everyday", tabs: everyday },
  ...modules.map((m) => ({ label: m.label, tabs: m.tabs })),
  { label: "Connect and configure", tabs: ["connectors", "members", "tokens", "webhooks", "logs", "audit"] },
];

export function Tools({ go }: { go: (t: Tab) => void }) {
  const { pins } = useNavPrefs();
  return (
    <div className="space-y-8">
      <PageHeader
        title="All tools"
        description="Everything Outflare does, grouped by module as in the sidebar. Open a tool, read its guide on outflare.ai, or pin it to the top of the sidebar. Agents can do all of it too, through the API and MCP."
        action={
          <a href={`${docsHost}/docs/`} target="_blank" rel="noreferrer" onClick={() => track("ui.docs_opened", { tab: "tools", from: "tools_header" })} className="inline-flex h-8 items-center gap-1.5 rounded-md border px-3 text-[13px] shadow-xs hover:bg-accent">
            <Icon name="book" className="size-3.5" /> All guides <Icon name="external" className="size-3 text-muted-foreground" />
          </a>
        }
      />
      {groups.map((g) => (
        <section key={g.label} aria-labelledby={`tools-${g.label}`} className="space-y-3">
          <h2 id={`tools-${g.label}`} className="font-mono text-[11px] tracking-wider text-muted-foreground uppercase">{g.label}</h2>
          <ul className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {g.tabs.map((t) => (
              <li key={t} className="flex flex-col rounded-lg border bg-card shadow-xs transition-colors hover:border-ring/50">
                <button onClick={() => go(t)} className="flex flex-1 items-start gap-3 rounded-t-lg p-4 text-left focus-visible:ring-[3px] focus-visible:ring-ring/40 focus-visible:outline-none">
                  <span className="flex size-8 shrink-0 items-center justify-center rounded-md border bg-background">
                    <Icon name={t as IconName} className="size-4 text-brand-strong dark:text-brand" />
                  </span>
                  <span className="min-w-0 space-y-1">
                    <span className="block text-[14px] font-medium">{tabNames[t]}</span>
                    <span className="block text-[13px] leading-snug text-muted-foreground">{tabBlurbs[t]}</span>
                  </span>
                </button>
                <div className="flex items-center justify-between border-t px-4 py-2 text-[12px] text-muted-foreground">
                  {tabDocs[t] ? (
                    <a href={`${docsHost}${tabDocs[t]}`} target="_blank" rel="noreferrer" onClick={() => track("ui.docs_opened", { tab: t, from: "tools" })} className="-my-2 inline-flex items-center gap-1 py-2 hover:text-foreground">
                      Read the guide <Icon name="external" className="size-3" />
                    </a>
                  ) : <span />}
                  <span className="flex items-center gap-3">
                    {shortcuts[t] && <span className="hidden font-mono text-[11px] sm:inline">G {shortcuts[t]!.toUpperCase()}</span>}
                    {!everyday.includes(t) && (
                      <button onClick={() => togglePin(t, "tools")} aria-pressed={pins.includes(t)} className={`-my-2 inline-flex items-center gap-1 py-2 hover:text-foreground ${pins.includes(t) ? "text-brand-strong dark:text-brand" : ""}`}>
                        <Icon name="pin" className="size-3" /> {pins.includes(t) ? "Pinned" : "Pin"}
                      </button>
                    )}
                  </span>
                </div>
              </li>
            ))}
          </ul>
        </section>
      ))}
    </div>
  );
}
