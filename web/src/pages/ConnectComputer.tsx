import { useState } from "react";
import { api, errorMessage } from "../api";
import { Button, CenteredForm, ErrorText, field } from "../ui";
import { cn } from "@/lib/utils";
import { track } from "@/lib/analytics";

// /connect/computer?code=ABCD-2345: Outflare for desktop opened this page so
// the person can link their computer. They check the code matches the one
// the app shows, pick the workspace, and approve; the app finishes by
// itself (POST /v1/workspaces/{ws}/ai-devices).

type Ws = { id: string; slug: string; name: string; scopes?: string[]; features?: string[] };

export function ConnectComputer({ workspaces, onDone }: { workspaces: Ws[]; onDone: () => void }) {
  const usable = workspaces.filter((w) => w.features?.includes("ai_accounts") && w.scopes?.includes("my_ai_accounts.manage"));
  const [code, setCode] = useState(() => new URLSearchParams(location.search).get("code") ?? "");
  const [ws, setWs] = useState(usable[0]?.id ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [linked, setLinked] = useState<{ name: string; slug: string } | null>(null);

  if (usable.length === 0) {
    return (
      <CenteredForm title="Outflare for desktop isn't on here" subtitle="None of your workspaces has AI Accounts switched on yet. Ask your workspace's admin.">
        <a href="/" className="block text-center text-[13px] underline">Go to Outflare</a>
      </CenteredForm>
    );
  }

  if (linked) {
    return (
      <CenteredForm title="Computer linked" subtitle={`${linked.name} now reports your AI usage. You can close this tab; Outflare for desktop carries on by itself.`}>
        <a href={`/${linked.slug}/settings/ai-accounts`} className="block text-center text-[13px] underline" onClick={onDone}>Open AI accounts</a>
      </CenteredForm>
    );
  }

  async function approve(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const { data, error } = await api.POST("/v1/workspaces/{ws}/ai-devices", { params: { path: { ws } }, body: { user_code: code } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    track("ui.ai_device_link_approved");
    setLinked({ name: (data as { name: string }).name, slug: usable.find((w) => w.id === ws)!.slug });
  }

  return (
    <CenteredForm title="Link this computer" subtitle="Outflare for desktop reads how much of each limit your AI tools have used and when they reset. Only those numbers reach Outflare.">
      <form onSubmit={approve} className="space-y-4">
        <label className="block text-[13px]">
          <span className="mb-1.5 block font-medium">Code shown in the app</span>
          <input className={cn(field, "w-full text-center font-mono text-lg tracking-[0.15em] uppercase")} value={code} onChange={(e) => setCode(e.target.value)}
            placeholder="ABCD-2345" autoComplete="off" spellCheck={false} required maxLength={9} aria-describedby="code-help" />
          <span id="code-help" className="mt-1 block text-[12px] text-muted-foreground">Check it matches exactly. Never enter a code someone else sent you.</span>
        </label>
        {usable.length > 1 ? (
          <label className="block text-[13px]">
            <span className="mb-1.5 block font-medium">Workspace</span>
            <select className={cn(field, "w-full")} value={ws} onChange={(e) => setWs(e.target.value)}>
              {usable.map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}
            </select>
          </label>
        ) : (
          <p className="text-[13px]">Workspace: <span className="font-medium">{usable[0].name}</span></p>
        )}
        <ErrorText>{error}</ErrorText>
        <Button className="w-full" disabled={busy || code.replace(/[^a-z0-9]/gi, "").length !== 8}>{busy ? "Linking…" : "Link computer"}</Button>
        <p className="text-center text-[12px] text-muted-foreground">Unlink it any time under AI accounts → Computers.</p>
      </form>
    </CenteredForm>
  );
}
