import { useEffect, useState, type ReactNode } from "react";
import type { AgentApp, Scope } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, CopyButton, PageHeader, toast } from "../ui";
import { Icon } from "../icons";
import { ago } from "../format";
import { track } from "@/lib/analytics";
import { cn } from "@/lib/utils";
import { AppMark } from "@/components/app-mark";
import { appLabels, hostedMcpUrl, reloadAgentApps, useAgentApps } from "@/lib/agentApps";
import { Snippet } from "./Tokens";
import { appSwitches } from "./Authorize";
import { summary } from "@/lib/permissions";
import { Switch } from "@/components/ui/switch";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import type { Tab } from "../App";

// Connect your AI: the one place that tells a person how to run Outflare
// from Claude, ChatGPT, Cursor or any MCP app. With the hosted MCP server
// it is one URL and a sign-in; the app's connection shows up here the
// moment it is approved.

type Client = "claude" | "chatgpt" | "claude_code" | "cursor" | "vscode" | "other";

const clients: { id: Client; label: string; mark: Parameters<typeof AppMark>[0]["app"]; sub: string }[] = [
  { id: "claude", label: "Claude", mark: "claude", sub: "Web, desktop, mobile" },
  { id: "chatgpt", label: "ChatGPT", mark: "chatgpt", sub: "On chatgpt.com" },
  { id: "claude_code", label: "Claude Code", mark: "claude", sub: "In your terminal" },
  { id: "cursor", label: "Cursor", mark: "cursor", sub: "One click" },
  { id: "vscode", label: "VS Code", mark: "vscode", sub: "Copilot agent mode" },
  { id: "other", label: "Other apps", mark: "other", sub: "Any MCP app" },
];

export const tryAsking = [
  "What does Outflare know about my company, and what's blocking my outreach?",
  "Which replies need an answer today? Draft answers for the interested ones.",
  "Import the leads in this file, check them against my suppression list and show me what's left.",
  "Write a three-step sequence for my main project and preview it for a real lead.",
];

function Steps({ children }: { children: ReactNode[] }) {
  return (
    <ol className="space-y-3">
      {children.map((c, i) => (
        <li key={i} className="flex gap-3 text-[13px] leading-relaxed">
          <span aria-hidden className="mt-0.5 flex size-5 shrink-0 items-center justify-center rounded-full border font-mono text-[11px] text-muted-foreground">{i + 1}</span>
          <div className="min-w-0 flex-1">{c}</div>
        </li>
      ))}
    </ol>
  );
}

// A prompt that has the person's AI app install Outflare itself: a coding
// agent runs the command, a chat app walks them through its settings.
export function setupPrompt(url: string) {
  return `Please connect Outflare to this app for me.

Outflare runs my marketing. Its MCP server is at ${url} (remote, Streamable HTTP). It signs in with OAuth in my browser, so there is no API key to set.

1. If you can run commands or edit your own settings, add it yourself:
   - Claude Code: claude mcp add --transport http --scope user outflare ${url}
     Then tell me to run /mcp, pick outflare and choose Authenticate.
   - Cursor: add "outflare": { "url": "${url}" } under "mcpServers" in ~/.cursor/mcp.json
   - VS Code: code --add-mcp '{"name":"outflare","type":"http","url":"${url}"}'
   - Anything else: use this app's own way to add a remote MCP server with that URL.
2. If you can't (claude.ai, Claude Desktop, ChatGPT), walk me through it one step at a time:
   - Claude: Settings, Connectors, Add custom connector. Name it Outflare and paste the URL.
   - ChatGPT (on chatgpt.com): Plugins, +, Add custom MCP server. Name it Outflare, paste the URL as the Server URL, pick OAuth, confirm, Create, then install the plugin. In a chat, type @ and pick Outflare.
3. Outflare opens in my browser to sign in: I pick the workspace and approve. Wait for me.
4. Once it's connected, call get_context and tell me in a few lines what Outflare knows about my company and what's blocking my outreach.`;
}

function SetupPrompt({ url }: { url: string }) {
  const prompt = setupPrompt(url);
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    await navigator.clipboard?.writeText(prompt);
    track("ui.copied");
    opened("prompt_copied");
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };
  const opened = (where: string) => track("ui.agent_install_opened", { client: "prompt", where });
  const link = "inline-flex h-8 items-center gap-1.5 rounded-md border px-3 text-[13px] shadow-xs hover:bg-accent";
  return (
    <section aria-labelledby="ai-prompt" className="space-y-3 rounded-lg border bg-card p-4 shadow-xs">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 id="ai-prompt" className="text-[13px] font-semibold">Or let your AI set it up</h2>
          <p className="text-[13px] text-muted-foreground">Paste this into Claude, Claude Code, Cursor or ChatGPT. It installs Outflare itself where it can, or walks you through each click.</p>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button onClick={copy}><Icon name={copied ? "check" : "copy"} className="size-3.5" /> {copied ? "Copied" : "Copy prompt"}</Button>
          <a href={`https://claude.ai/new?q=${encodeURIComponent(prompt)}`} target="_blank" rel="noreferrer" onClick={() => opened("prompt_claude")} className={link}>
            <AppMark app="claude" className="size-4 rounded-sm border-0 p-0" /> Ask Claude
          </a>
          <a href={`https://chatgpt.com/?q=${encodeURIComponent(prompt)}`} target="_blank" rel="noreferrer" onClick={() => opened("prompt_chatgpt")} className={link}>
            <AppMark app="chatgpt" className="size-4 rounded-sm border-0 p-0" /> Ask ChatGPT
          </a>
        </div>
      </div>
      <pre className="max-h-32 overflow-y-auto rounded-md border bg-muted/40 px-3 py-2 font-mono text-[11.5px] leading-relaxed whitespace-pre-wrap text-muted-foreground">{prompt}</pre>
    </section>
  );
}

const b = (s: string) => <span className="font-medium text-foreground">{s}</span>;

export function UrlBox({ url, big }: { url: string; big?: boolean }) {
  return (
    <div className={cn("flex min-w-0 items-center gap-2 rounded-md border bg-muted/40 pl-3", big ? "h-11" : "h-9")}>
      <code className={cn("min-w-0 flex-1 truncate font-mono", big ? "text-[14px]" : "text-[12px]")}>{url}</code>
      <CopyButton text={url} label="Copy the MCP server URL" className="mr-1.5 size-8" />
    </div>
  );
}

function Instructions({ client, url }: { client: Client; url: string }) {
  const opened = (where: string) => track("ui.agent_install_opened", { client, where });
  switch (client) {
    case "claude":
      return (
        <Steps>
          <>In Claude, open {b("Settings → Connectors")} and choose {b("Add custom connector")}. <a href="https://claude.ai/settings/connectors" target="_blank" rel="noreferrer" onClick={() => opened("claude_settings")} className="inline-flex items-center gap-1 underline">Open Claude's connectors <Icon name="external" className="size-3" /></a></>
          <>Name it {b("Outflare")} and paste this URL: <div className="mt-2"><UrlBox url={url} /></div></>
          <>Click {b("Connect")}. Claude sends you here to sign in: pick the workspace and approve.</>
          <>In a chat, turn on {b("Outflare")} in the tools menu and ask it something. On a Team or Enterprise plan, an owner adds the connector once under {b("Organization settings → Connectors")}, then everyone connects their own account.</>
        </Steps>
      );
    case "chatgpt":
      return (
        <Steps>
          <>On chatgpt.com, open {b("Plugins")}, click {b("+")} and choose {b("Add custom MCP server")}. <a href="https://chatgpt.com/" target="_blank" rel="noreferrer" onClick={() => opened("chatgpt")} className="inline-flex items-center gap-1 underline">Open ChatGPT <Icon name="external" className="size-3" /></a></>
          <>Name it {b("Outflare")}, paste this as the {b("Server URL")} and choose {b("OAuth")} for authentication: <div className="mt-2"><UrlBox url={url} /></div></>
          <>Click {b("I understand and want to continue")}, then {b("Create")}. Install the Outflare plugin: ChatGPT sends you here to pick the workspace and approve.</>
          <>In a new chat, type {b("@")}, pick {b("Outflare")} and ask it something. On a Business, Enterprise or Edu workspace, an admin first allows {b("Create plugins with MCPs")} under {b("Workspace settings → Permissions & roles")}.</>
        </Steps>
      );
    case "claude_code": {
      const cmd = `claude mcp add --transport http outflare ${url}`;
      return (
        <Steps>
          <>Add the server: <div className="mt-2"><Snippet title="terminal" code={cmd} /></div></>
          <>In Claude Code, run {b("/mcp")}, pick {b("outflare")} and choose {b("Authenticate")}. Your browser opens here to approve.</>
          <>Ask Claude Code anything about your outreach. Add {b("--scope user")} to the command to have Outflare in every project.</>
        </Steps>
      );
    }
    case "cursor": {
      const deeplink = `cursor://anysphere.cursor-deeplink/mcp/install?name=outflare&config=${btoa(JSON.stringify({ url }))}`;
      return (
        <Steps>
          <>
            <a href={deeplink} onClick={() => opened("cursor_deeplink")} className="inline-flex h-8 items-center gap-2 rounded-md bg-primary px-3 text-[13px] font-medium text-primary-foreground shadow-xs hover:bg-primary/90">
              <AppMark app="cursor" className="size-5 rounded border-0 p-0.5" /> Add to Cursor
            </a>
            <span className="ml-2 text-muted-foreground">or add it to {b("~/.cursor/mcp.json")}:</span>
            <div className="mt-2"><Snippet title="mcp.json" code={JSON.stringify({ mcpServers: { outflare: { url } } }, null, 2)} /></div>
          </>
          <>In {b("Cursor Settings → Tools & MCP")}, click {b("Connect")} next to outflare and approve here.</>
          <>Ask the agent in chat. It sees every Outflare tool.</>
        </Steps>
      );
    }
    case "vscode": {
      const deeplink = `vscode:mcp/install?${encodeURIComponent(JSON.stringify({ name: "outflare", type: "http", url }))}`;
      return (
        <Steps>
          <>
            <a href={deeplink} onClick={() => opened("vscode_deeplink")} className="inline-flex h-8 items-center gap-2 rounded-md bg-primary px-3 text-[13px] font-medium text-primary-foreground shadow-xs hover:bg-primary/90">
              <AppMark app="vscode" className="size-5 rounded border-0 p-0.5" /> Add to VS Code
            </a>
            <span className="ml-2 text-muted-foreground">or run:</span>
            <div className="mt-2"><Snippet title="terminal" code={`code --add-mcp '${JSON.stringify({ name: "outflare", type: "http", url })}'`} /></div>
          </>
          <>VS Code asks you to sign in when the server starts: approve here.</>
          <>In Copilot Chat, switch to {b("Agent")} mode and ask.</>
        </Steps>
      );
    }
    default:
      return (
        <Steps>
          <>Any app that connects to remote MCP servers can use Outflare (Windsurf, Codex, Gemini CLI, Goose, Zed and more). Give it this URL: <div className="mt-2"><UrlBox url={url} /></div></>
          <>Apps that sign in with OAuth send you here to approve. That is the safest way: nothing to copy, and you can disconnect the app below.</>
          <>An app that can't sign in can send an API token instead, as the header {b("Authorization: Bearer oft_...")}. Create one under Settings, API tokens, and choose what it may do.</>
        </Steps>
      );
  }
}

export function ConnectedApps({ ws, apps, canSeeAll, open, setOpen }: { ws: string; apps: AgentApp[]; canSeeAll: boolean; open?: string; setOpen: (id: string | undefined) => void }) {
  const shown = apps.find((a) => a.id === open);
  return (
    <>
      <ul className="divide-y rounded-lg border bg-card">
        {apps.map((a) => (
          <li key={a.id}>
            <button type="button" onClick={() => setOpen(a.id)} className="flex w-full flex-wrap items-center gap-3 px-4 py-3 text-left hover:bg-muted/40">
              <AppMark app={a.app} />
              <span className="min-w-0 flex-1">
                <span className="flex flex-wrap items-center gap-2 font-medium">
                  {a.name}
                  {a.app !== "unknown" && a.app !== "local" && appLabels[a.app] !== a.name && <span className="text-[12px] font-normal text-muted-foreground">{appLabels[a.app]}</span>}
                  <Badge tone={summary(a.scopes) === "Read only" ? "grey" : "green"}>{summary(a.scopes)}</Badge>
                  {!a.verified && <Badge tone="amber">Not verified</Badge>}
                </span>
                <span className="block text-[12px] text-muted-foreground">
                  {canSeeAll ? `${a.user_name} connected it ` : "Connected "}
                  {ago(a.created_at)}
                  {a.last_used_at ? ` · last used ${ago(a.last_used_at)}` : " · not used yet"}
                </span>
              </span>
              <span className="inline-flex items-center gap-1 text-[13px] text-muted-foreground">Permissions <Icon name="chevron" className="size-3.5" /></span>
            </button>
          </li>
        ))}
      </ul>
      {shown && <AppSettings ws={ws} app={shown} onClose={() => setOpen(undefined)} />}
    </>
  );
}

// AppSettings is a connected app's permissions: the switches from the
// approval screen, saved as they flip, and Disconnect.
function AppSettings({ ws, app: a, onClose }: { ws: string; app: AgentApp; onClose: () => void }) {
  const [scopes, setScopes] = useState<string[]>(a.scopes);
  const [mission, setMission] = useState(a.require_mission);
  const [busy, setBusy] = useState(false);
  useEffect(() => setScopes(a.scopes), [a.scopes]);
  useEffect(() => setMission(a.require_mission), [a.require_mission]);
  const switches = appSwitches.map((g) => ({ ...g, scopes: g.scopes.filter((s) => a.available_scopes.includes(s as Scope)) })).filter((g) => g.scopes.length > 0);
  const writes = scopes.filter((s) => !s.endsWith(".read"));

  async function save(next: string[], done: string) {
    setBusy(true);
    const prev = scopes;
    setScopes([...scopes.filter((s) => s.endsWith(".read")), ...next]);
    const { error } = await api.PATCH("/v1/workspaces/{ws}/agent-apps/{id}", { params: { path: { ws, id: a.id } }, body: { scopes: next as Scope[] } });
    setBusy(false);
    if (error) {
      setScopes(prev);
      return toast(errorMessage(error), "error");
    }
    toast(done);
    reloadAgentApps(ws);
  }
  async function saveMission(on: boolean) {
    setBusy(true);
    setMission(on);
    const { error } = await api.PATCH("/v1/workspaces/{ws}/agent-apps/{id}", { params: { path: { ws, id: a.id } }, body: { require_mission: on } });
    setBusy(false);
    if (error) {
      setMission(!on);
      return toast(errorMessage(error), "error");
    }
    toast(on ? `${a.name} makes changes only inside missions you approve.` : `${a.name} may make changes outside missions.`);
    reloadAgentApps(ws);
  }
  const flip = (g: (typeof switches)[number], on: boolean) =>
    save(on ? [...new Set([...writes, ...g.scopes])] : writes.filter((s) => !g.scopes.includes(s)), `${g.label}: ${on ? "on" : "off"} for ${a.name}.`);

  async function disconnect() {
    if (!(await confirmAction(`Disconnect ${a.name}?`, <p>It stops working at once. You can connect it again any time.</p>, "Disconnect"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/agent-apps/{id}", { params: { path: { ws, id: a.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast(`Disconnected ${a.name}.`);
    onClose();
    reloadAgentApps(ws);
  }

  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-md">
        <SheetHeader>
          <SheetTitle className="flex items-center gap-2.5"><AppMark app={a.app} className="size-7" />{a.name}</SheetTitle>
          <SheetDescription>
            {a.user_name} connected it {ago(a.created_at)}{a.last_used_at ? `, last used ${ago(a.last_used_at)}` : ""}. Changes apply on its next request, with no need to connect again.
          </SheetDescription>
        </SheetHeader>
        <div className="space-y-5 px-4 pb-6">
          <fieldset className="space-y-2">
            <div className="flex items-baseline justify-between">
              <legend className="text-[13px] font-medium">What {a.name} may do</legend>
              {switches.length > 0 && (
                <span className="flex gap-3 text-[12px] text-muted-foreground">
                  <button type="button" disabled={busy} className="underline-offset-2 hover:underline" onClick={() => save(switches.filter((g) => g.id !== "spend").flatMap((g) => g.scopes), `${a.name} may make changes again.`)}>Everything</button>
                  <button type="button" disabled={busy} className="underline-offset-2 hover:underline" onClick={() => save([], `${a.name} is read only now.`)}>Read only</button>
                </span>
              )}
            </div>
            <div className="divide-y rounded-md border">
              <div className="flex items-center gap-3 px-3 py-2.5">
                <span className="min-w-0 flex-1">
                  <span className="block text-[13px] font-medium">See your workspace</span>
                  <span className="block text-[12px] text-muted-foreground">Read everything {a.user_name} can see. Always on.</span>
                </span>
                <Switch checked disabled aria-label="See your workspace" />
              </div>
              {switches.map((g) => (
                <label key={g.id} className="flex cursor-pointer items-center gap-3 px-3 py-2.5">
                  <span className="min-w-0 flex-1">
                    <span className="block text-[13px] font-medium">{g.label}</span>
                    <span className="block text-[12px] text-muted-foreground">{g.help}</span>
                  </span>
                  <Switch checked={g.scopes.some((s) => scopes.includes(s))} disabled={busy} onCheckedChange={(on) => flip(g, on)} aria-label={g.label} />
                </label>
              ))}
            </div>
            <p className="flex gap-2 text-[12px] text-muted-foreground">
              <Icon name="x" className="mt-0.5 size-3.5 shrink-0" />
              It can never invite or remove people, create API tokens, approve its own missions, or send around your suppression list and send limits.
            </p>
            {writes.length === 0 && a.verified && <p className="text-[12px] text-muted-foreground">Turned something on? Start a new chat in {a.name} so it picks up the tools that make changes.</p>}
          </fieldset>
          {a.verified ? (
            <label className="flex cursor-pointer items-center gap-3 rounded-md border px-3 py-2.5">
              <span className="min-w-0 flex-1">
                <span className="block text-[13px] font-medium">Only inside missions</span>
                <span className="block text-[12px] text-muted-foreground">It makes changes only as part of a mission you approved, so you see its plan first. Off lets it change things directly.</span>
              </span>
              <Switch checked={mission} disabled={busy} onCheckedChange={saveMission} aria-label="Only inside missions" />
            </label>
          ) : (
            <p role="note" className="rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-[12px] text-amber-800 dark:text-amber-200">
              Outflare doesn't recognise this app, so it can only read. The person who runs this Outflare can check it and let it make changes: <span className="font-mono">outflare verify-app {a.client_id}</span>
            </p>
          )}
          <div className="border-t pt-4">
            <Button variant="danger" onClick={disconnect}>Disconnect {a.name}</Button>
          </div>
        </div>
      </SheetContent>
    </Sheet>
  );
}

export function ConnectAI({ ws, canSeeAll, go, open, setOpen }: { ws: string; canSeeAll: boolean; go: (t: Tab) => void; open?: string; setOpen: (id: string | undefined) => void }) {
  const apps = useAgentApps(ws);
  const [url, setUrl] = useState<string | null>(null);
  const [client, setClient] = useState<Client>("claude");
  useEffect(() => {
    hostedMcpUrl().then(setUrl);
  }, []);

  // Until an app connects, look every few seconds, so the page answers the
  // moment the person approves in the other tab.
  const count = apps?.length ?? 0;
  useEffect(() => {
    if (apps === null || count > 0) return;
    const t = setInterval(() => document.visibilityState === "visible" && reloadAgentApps(ws), 4000);
    return () => clearInterval(t);
  }, [ws, apps === null, count]); // eslint-disable-line react-hooks/exhaustive-deps
  const [seen, setSeen] = useState<number | null>(null);
  useEffect(() => {
    if (apps === null) return;
    if (seen !== null && apps.length > seen) toast(`${apps[0].name} is connected. Ask it something.`);
    setSeen(apps.length);
  }, [apps]); // eslint-disable-line react-hooks/exhaustive-deps

  const pick = (c: Client) => {
    setClient(c);
    track("ui.agent_client_picked", { client: c });
  };

  return (
    <div className="space-y-6">
      <PageHeader
        title="Connect your AI"
        description="Run Outflare by asking Claude, ChatGPT or any AI app. It signs in with your Outflare account, so there is no key to copy, it only does what you allow, and you can disconnect it here any time."
        action={
          <a href="https://outflare.ai/docs/agents/mcp/" target="_blank" rel="noreferrer" onClick={() => track("ui.docs_opened", { tab: "ai", from: "ai_header" })} className="inline-flex h-8 items-center gap-1.5 rounded-md border px-3 text-[13px] shadow-xs hover:bg-accent">
            <Icon name="book" className="size-3.5" /> Guide <Icon name="external" className="size-3 text-muted-foreground" />
          </a>
        }
      />

      {url === "" ? (
        <Card title="This server runs the MCP server on your computer" description="It has no hosted MCP address (OUTFLARE_MCP_SERVER is not set).">
          <div className="space-y-3 text-[13px]">
            <p>Create an API token, then add the MCP server to your app with the token. The guide has the exact config for Claude Desktop, Claude Code and Cursor.</p>
            <div className="flex flex-wrap gap-2">
              <Button onClick={() => go("tokens")}>Create an API token</Button>
              <a href="https://outflare.ai/docs/agents/mcp/" target="_blank" rel="noreferrer" className="inline-flex h-8 items-center gap-1.5 rounded-md border px-3 text-[13px] shadow-xs hover:bg-accent">Read the guide <Icon name="external" className="size-3" /></a>
            </div>
          </div>
        </Card>
      ) : (
        <>
          <section aria-labelledby="ai-url" className="space-y-2 rounded-lg border bg-card p-4 shadow-xs">
            <h2 id="ai-url" className="text-[13px] font-semibold">Your Outflare MCP server</h2>
            <p className="text-[13px] text-muted-foreground">Every app connects with this one address. Pick your app below for the steps.</p>
            {url ? <UrlBox url={url} big /> : <div className="h-11 animate-pulse rounded-md bg-muted" />}
          </section>

          {url && <SetupPrompt url={url} />}

          <section aria-labelledby="ai-pick" className="space-y-3">
            <h2 id="ai-pick" className="font-mono text-[11px] tracking-wider text-muted-foreground uppercase">Which app do you use?</h2>
            <div role="tablist" className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-6">
              {clients.map((c) => (
                <button
                  key={c.id}
                  role="tab"
                  aria-selected={client === c.id}
                  onClick={() => pick(c.id)}
                  className={cn(
                    "flex min-h-14 items-center gap-2.5 rounded-lg border bg-card px-3 py-2 text-left shadow-xs transition-colors hover:border-ring/60",
                    client === c.id && "border-brand-strong ring-1 ring-brand-strong dark:border-brand dark:ring-brand",
                  )}
                >
                  <AppMark app={c.mark} className="size-8" />
                  <span className="min-w-0">
                    <span className="block text-[13px] font-medium">{c.label}</span>
                    <span className="block truncate text-[11px] text-muted-foreground">{c.sub}</span>
                  </span>
                </button>
              ))}
            </div>
            <div role="tabpanel" className="rounded-lg border bg-card p-4 shadow-xs">{url && <Instructions client={client} url={url} />}</div>
          </section>
        </>
      )}

      <section aria-labelledby="ai-connected" className="space-y-3">
        <h2 id="ai-connected" className="font-mono text-[11px] tracking-wider text-muted-foreground uppercase">{canSeeAll ? "Connected apps in this workspace" : "Your connected apps"}</h2>
        {apps === null ? null : apps.length === 0 ? (
          <div className="flex items-center gap-3 rounded-lg border border-dashed px-4 py-4 text-[13px] text-muted-foreground">
            <span aria-hidden className="relative flex size-2.5"><span className="absolute inline-flex size-full animate-ping rounded-full bg-brand opacity-60" /><span className="relative inline-flex size-2.5 rounded-full bg-brand" /></span>
            Waiting for your first app. Once you approve it, it shows up here.
          </div>
        ) : (
          <ConnectedApps ws={ws} apps={apps} canSeeAll={canSeeAll} open={open} setOpen={setOpen} />
        )}
      </section>

      {canSeeAll && (
        <div className="flex flex-wrap items-center gap-3 rounded-lg border bg-card px-4 py-3 text-[13px] shadow-xs">
          <Icon name="tokens" className="size-4 text-muted-foreground" />
          <span className="min-w-0 flex-1"><span className="font-medium">API tokens</span> <span className="text-muted-foreground">· for scripts, integrations and apps that can't sign in. Create one, or change what one may do.</span></span>
          <Button variant="ghost" onClick={() => go("tokens")}>API tokens</Button>
        </div>
      )}

      <Card title="Then just ask" description="The agent reads what Outflare knows about your company first, so you never repeat yourself.">
        <ul className="grid gap-2 sm:grid-cols-2">
          {tryAsking.map((q) => (
            <li key={q} className="flex items-start gap-2 rounded-md border bg-muted/30 px-3 py-2 text-[13px]">
              <span className="flex-1">{q}</span>
              <CopyButton text={q} label="Copy this prompt" />
            </li>
          ))}
        </ul>
      </Card>
    </div>
  );
}
