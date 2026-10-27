import { useEffect, useState, type FormEvent } from "react";
import type { Workspace } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, Card, ErrorText, field, Input, PageHeader, toast } from "../ui";
import { cn } from "@/lib/utils";
import { CopyButton } from "./Tokens";
import { AIModelCard } from "./AIModel";
import { SemanticSearchCard } from "./SemanticSearch";
import { SiteKeysCard } from "./SiteKeys";

// What someone types becomes a slug as they type: "Acme Co" → "acme-co".
const tidy = (s: string) => s.toLowerCase().replace(/[^a-z0-9-]+/g, "-").replace(/-{2,}/g, "-").replace(/^-/, "").slice(0, 40);

export function WorkspaceSettings({ ws, canManage, onSaved }: { ws: Workspace; canManage: boolean; onSaved: () => void }) {
  const [name, setName] = useState(ws.name);
  const [slug, setSlug] = useState(ws.slug);
  const [check, setCheck] = useState<{ slug: string; available: boolean; reason: string | null } | null>(null);
  const [busy, setBusy] = useState<"" | "name" | "slug">("");
  const [error, setError] = useState({ name: "", slug: "" });
  const host = location.host;
  const path = { params: { path: { ws: ws.id } } };
  const slugChanged = slug.replace(/-$/, "") !== ws.slug;

  useEffect(() => setName(ws.name), [ws.name]);
  useEffect(() => setSlug(ws.slug), [ws.slug]);

  // Check the slug a moment after typing stops.
  useEffect(() => {
    if (!canManage || !slugChanged) return setCheck(null);
    const want = slug.replace(/-$/, "");
    const t = setTimeout(async () => {
      const { data } = await api.GET("/v1/workspaces/{ws}/slug-availability", { params: { path: { ws: ws.id }, query: { slug: want } } });
      if (data) setCheck(data);
    }, 300);
    return () => clearTimeout(t);
  }, [slug, slugChanged, canManage, ws.id]);

  async function saveName(e: FormEvent) {
    e.preventDefault();
    setBusy("name");
    const { error } = await api.PATCH("/v1/workspaces/{ws}", { ...path, body: { name: name.trim() } });
    setBusy("");
    setError((x) => ({ ...x, name: error ? errorMessage(error) : "" }));
    if (!error) {
      toast("Workspace renamed.");
      onSaved();
    }
  }

  async function saveSlug(e: FormEvent) {
    e.preventDefault();
    const want = slug.replace(/-$/, "");
    setBusy("slug");
    const { error } = await api.PATCH("/v1/workspaces/{ws}", { ...path, body: { slug: want } });
    setBusy("");
    setError((x) => ({ ...x, slug: error ? errorMessage(error) : "" }));
    if (!error) {
      toast(`This workspace is now at ${host}/${want}.`);
      onSaved();
    }
  }

  const slugOK = !slugChanged || (check?.slug === slug.replace(/-$/, "") && !!check?.available);

  return (
    <div className="max-w-2xl space-y-6">
      <PageHeader title="General" description={canManage ? "The workspace's name, its address in the app, and the AI model missions use." : "Only owners and admins can change these."} />

      <Card title="Name" description="Shown in the sidebar, in emails about this workspace and to everyone in it.">
        <form onSubmit={saveName} className="flex flex-col gap-3 sm:flex-row sm:items-end">
          <div className="flex-1">
            <Input label="Workspace name" value={name} maxLength={100} disabled={!canManage} onChange={(e) => setName(e.target.value)} required />
          </div>
          {canManage && <Button type="submit" disabled={busy !== "" || !name.trim() || name.trim() === ws.name}>{busy === "name" ? "Saving…" : "Save"}</Button>}
        </form>
        <ErrorText>{error.name}</ErrorText>
      </Card>

      <Card title="URL" description="Where this workspace lives in the app. If you change it, links with the old URL still work until another workspace takes it.">
        <form onSubmit={saveSlug} className="flex flex-col gap-3 sm:flex-row sm:items-end">
          <label className="block flex-1 text-sm">
            <span className="mb-1.5 block text-[13px] font-medium">Workspace URL</span>
            <span className={cn(field, "flex w-full items-center gap-0 px-0 focus-within:border-ring focus-within:ring-[3px] focus-within:ring-ring/40", check && !check.available && "border-destructive")}>
              <span className="pl-2.5 text-muted-foreground select-none">{host}/</span>
              <input
                className="h-full min-w-0 flex-1 bg-transparent pr-2.5 font-mono text-[13px] outline-none disabled:cursor-not-allowed"
                value={slug}
                disabled={!canManage}
                spellCheck={false}
                autoCapitalize="off"
                autoComplete="off"
                aria-invalid={!!check && !check.available}
                aria-describedby="slug-help"
                onChange={(e) => setSlug(tidy(e.target.value))}
                required
              />
            </span>
            <span id="slug-help" aria-live="polite" className={cn("mt-1 block text-xs", check && !check.available ? "text-destructive" : "text-muted-foreground")}>
              {!slugChanged ? "Lowercase letters, numbers and dashes." : !check || check.slug !== slug.replace(/-$/, "") ? "Checking…" : check.available ? `${host}/${check.slug} is free.` : check.reason?.replace(/^slug /, "The URL ")}
            </span>
          </label>
          {canManage && <Button type="submit" className="sm:mb-5" disabled={busy !== "" || !slugChanged || !slugOK}>{busy === "slug" ? "Saving…" : "Save"}</Button>}
        </form>
        <ErrorText>{error.slug}</ErrorText>
      </Card>

      <AIModelCard ws={ws.id} canManage={canManage} />

      <SemanticSearchCard ws={ws.id} canManage={canManage} />

      <SiteKeysCard ws={ws.id} canManage={canManage} />

      <TestWorkspaceCard current={ws} />

      <Card title="Workspace ID" description="For the API and MCP. It never changes, so use it in scripts and integrations; the URL name works in API paths too.">
        <div className="flex items-center gap-2">
          <code className="min-w-0 flex-1 truncate rounded-md border bg-muted/40 px-2.5 py-1.5 font-mono text-[12px]">{ws.id}</code>
          <CopyButton text={ws.id} />
        </div>
      </Card>
    </div>
  );
}

// A test workspace is the sandbox: simulated mailboxes, so sequences,
// replies and bounces can be tried without emailing anyone.
function TestWorkspaceCard({ current }: { current: Workspace }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function create() {
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces", { body: { name: `${current.name} (test)`.slice(0, 100), test: true, sample_data: true } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    location.assign(`/${data.slug}/overview`);
  }

  if (current.test) {
    return (
      <Card title="Test workspace" description="Nothing here reaches anyone. Mailboxes are simulated, sends are recorded but never delivered, verification is made up from the address and nothing can be bought.">
        <p className="text-[13px] text-muted-foreground">
          Try a sequence, then answer it from the API or MCP: <code className="font-mono text-[12px]">POST /test/send-now</code> sends every due step at once, and{" "}
          <code className="font-mono text-[12px]">POST /test/inbound</code> plays a reply, auto-reply, opt-out or bounce.
        </p>
      </Card>
    );
  }
  return (
    <Card title="Test workspace" description="A separate workspace where nothing reaches anyone: simulated mailboxes, sample leads, and replies and bounces on demand. Use it to try sequences or build an integration.">
      <Button variant="ghost" onClick={create} disabled={busy}>{busy ? "Creating…" : "Create a test workspace"}</Button>
      <ErrorText>{error}</ErrorText>
    </Card>
  );
}
