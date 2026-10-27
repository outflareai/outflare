import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { Button, CenteredForm, ErrorText, field, Loading } from "../ui";
import { OutflareMark } from "@/brand";
import { Icon } from "../icons";
import { cn } from "@/lib/utils";
import { AppMark } from "@/components/app-mark";
import { Switch } from "@/components/ui/switch";
import { appLabels } from "@/lib/agentApps";
import type { AgentAppKind } from "@outflare/sdk";
import { areas } from "@/lib/permissions";

// The OAuth consent screen at /oauth/authorize: an AI app (Claude, ChatGPT,
// Cursor...) asks to use the person's Outflare account. They pick the
// workspace and how much it may do; approving sends them back to the app.
// The engine has already checked the app and its redirect URI.

type Info = {
  client: { id: string; name: string; uri: string };
  app: AgentAppKind;
  verified: boolean;
  redirect_host: string;
  resource: string;
  workspaces: { id: string; slug: string; name: string; role: string; full_scopes: string[]; read_scopes: string[]; can_spend: boolean; brands: { id: string; handle: string; name: string }[] | null }[];
};

// What the person can switch on or off for the app: what each area may
// change, plus spending. Reading is always on.
export const appSwitches: { id: string; label: string; help: string; scopes: string[] }[] = [
  ...areas.filter((a) => a.write.length > 0).map((a) => ({ id: a.id, label: a.label, help: a.help, scopes: a.write as string[] })),
  { id: "spend", label: "Spend money", help: "Buy domains and mailboxes without asking. Off means you approve each purchase in Outflare.", scopes: ["infrastructure.approve"] },
];
const groups = appSwitches;
// Spending starts off: buying something is the one step that can't be undone.
const startsOff = groups.filter((g) => g.id === "spend").map((g) => g.id);

export function Authorize({ email, onSignOut }: { email?: string; onSignOut: () => void }) {
  const q = new URLSearchParams(location.search);
  const [info, setInfo] = useState<Info | null>(null);
  const [error, setError] = useState("");
  const [ws, setWs] = useState("");
  const [off, setOff] = useState<Set<string>>(() => new Set(startsOff));
  const [brand, setBrand] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api.GET("/v1/agent-apps/authorize", { params: { query: { client_id: q.get("client_id") ?? "", redirect_uri: q.get("redirect_uri") ?? "" } } }).then(({ data, error }) => {
      if (error) return setError(errorMessage(error));
      setInfo(data as Info);
      setWs(data!.workspaces[0]?.id ?? "");
    });
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  // One brand needs no picking.
  const brandFor = (id: string) => {
    const list = info?.workspaces.find((w) => w.id === id)?.brands;
    return brand || (list?.length === 1 ? list[0].id : "");
  };
  // The groups this person's role lets them hand an app in workspace id.
  const offered = (id: string) => {
    const w = info?.workspaces.find((v) => v.id === id);
    // An app nobody has checked may only read.
    if (!w || !info?.verified) return [];
    const has = (sc: string) => w.full_scopes.includes(sc) || (sc === "infrastructure.approve" && w.can_spend);
    return groups.filter((g) => g.scopes.some(has)).map((g) => ({ ...g, scopes: g.scopes.filter(has) }));
  };

  async function answer(approve: boolean) {
    setBusy(true);
    const { data, error } = await api.POST("/v1/agent-apps/authorize", {
      body: {
        client_id: q.get("client_id") ?? "",
        redirect_uri: q.get("redirect_uri") ?? "",
        state: q.get("state") ?? undefined,
        code_challenge: q.get("code_challenge") ?? undefined,
        code_challenge_method: "S256",
        workspace_id: ws,
        scopes: offered(ws).filter((g) => !off.has(g.id)).flatMap((g) => g.scopes),
        brand: info?.workspaces.find((w) => w.id === ws)?.brands ? brandFor(ws) : undefined,
        approve,
      },
    });
    if (error) {
      setBusy(false);
      return setError(errorMessage(error));
    }
    location.assign(data!.redirect_url);
  }

  if (error && !info) {
    return (
      <CenteredForm title="This connection link isn't valid" subtitle="Start connecting again from your AI app.">
        <ErrorText>{error}</ErrorText>
        <a href="/" className="block text-center text-[13px] underline">Go to Outflare</a>
      </CenteredForm>
    );
  }
  if (!info) return <CenteredForm title="Connect your AI"><Loading /></CenteredForm>;

  const known = info.app !== "unknown";
  const appName = info.app === "claude" || info.app === "chatgpt" || info.app === "cursor" || info.app === "vscode" ? appLabels[info.app] : info.client.name;
  const chosen = info.workspaces.find((w) => w.id === ws);
  const toggles = offered(ws);
  const flip = (id: string, on: boolean) => setOff((prev) => { const next = new Set(prev); if (on) next.delete(id); else next.add(id); return next; });
  if (info.workspaces.length === 0) {
    return (
      <CenteredForm title="No workspace to connect" subtitle="You aren't in a workspace yet. Create one, or ask to be invited, then connect again.">
        <a href="/" className="block text-center text-[13px] underline">Go to Outflare</a>
      </CenteredForm>
    );
  }

  return (
    <CenteredForm title={`Connect ${appName} to Outflare`} subtitle={`${appName} wants to work in your Outflare workspace for you.`}>
      <div className="flex items-center justify-center py-2" aria-hidden>
        <AppMark app={info.app} className="size-14 rounded-xl p-2.5 [&_svg]:size-7" />
        <span className="flex w-16 items-center">
          <span className="h-px flex-1 border-t border-dashed border-muted-foreground/40" />
          <span className="flex size-6 items-center justify-center rounded-full border bg-background"><Icon name="check" className="size-3 text-green-600 dark:text-green-400" /></span>
          <span className="h-px flex-1 border-t border-dashed border-muted-foreground/40" />
        </span>
        <span className="flex size-14 items-center justify-center rounded-xl border bg-background"><OutflareMark className="h-7 w-auto" /></span>
      </div>

      {!known && (
        <div role="alert" className="rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-[12px] text-amber-800 dark:text-amber-200">
          <span className="font-medium">Outflare doesn't recognise this app.</span> It calls itself "{info.client.name}" and sends you back to <span className="font-mono">{info.redirect_host}</span>. Only continue if you started connecting from there.
          {!info.verified && " Until the person who runs this Outflare checks it, it can only read."}
        </div>
      )}

      {info.workspaces.length > 1 ? (
        <label className="block text-[13px]">
          <span className="mb-1.5 block font-medium">Workspace</span>
          <select className={cn(field, "w-full")} value={ws} onChange={(e) => { setWs(e.target.value); setBrand(""); }}>
            {info.workspaces.map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}
          </select>
        </label>
      ) : (
        <p className="text-[13px]">Workspace: <span className="font-medium">{chosen?.name}</span></p>
      )}

      {chosen?.brands && (
        <label className="block text-[13px]">
          <span className="mb-1.5 block font-medium">Brand</span>
          <select className={cn(field, "w-full")} value={brandFor(ws)} onChange={(e) => setBrand(e.target.value)}>
            <option value="" disabled>Pick the brand it works on</option>
            {chosen.brands.map((b) => <option key={b.id} value={b.id}>{b.name || b.handle}</option>)}
          </select>
          <span className="mt-1 block text-[12px] text-muted-foreground">You work on some brands here, so the app is kept to one of them.</span>
        </label>
      )}

      <fieldset className="space-y-2">
        <div className="flex items-baseline justify-between">
          <legend className="text-[13px] font-medium">What {appName} may do</legend>
          {toggles.length > 0 && (
            <span className="flex gap-3 text-[12px] text-muted-foreground">
              <button type="button" className="underline-offset-2 hover:underline" onClick={() => setOff(new Set(startsOff))}>Everything</button>
              <button type="button" className="underline-offset-2 hover:underline" onClick={() => setOff(new Set(groups.map((g) => g.id)))}>Read only</button>
            </span>
          )}
        </div>
        <div className="divide-y rounded-md border">
          <div className="flex items-center gap-3 px-3 py-2.5">
            <span className="min-w-0 flex-1">
              <span className="block text-[13px] font-medium">See your workspace</span>
              <span className="block text-[12px] text-muted-foreground">Read leads, sequences, replies, mailboxes and results. Always on.</span>
            </span>
            <Switch checked disabled aria-label="See your workspace" />
          </div>
          {toggles.map((g) => (
            <label key={g.id} className="flex cursor-pointer items-center gap-3 px-3 py-2.5">
              <span className="min-w-0 flex-1">
                <span className="block text-[13px] font-medium">{g.label}</span>
                <span className="block text-[12px] text-muted-foreground">{g.help}</span>
              </span>
              <Switch checked={!off.has(g.id)} onCheckedChange={(on) => flip(g.id, on)} aria-label={g.label} />
            </label>
          ))}
        </div>
        <p className="flex gap-2 text-[12px] text-muted-foreground">
          <Icon name="x" className="mt-0.5 size-3.5 shrink-0" />
          It can never invite or remove people, create API tokens, approve its own missions, or send around your suppression list and send limits.
        </p>
        {toggles.length > 0 && (
          <p className="flex gap-2 text-[12px] text-muted-foreground">
            <Icon name="check" className="mt-0.5 size-3.5 shrink-0" />
            It makes changes only inside a mission you approve. You can turn this off for it under Connect your AI.
          </p>
        )}
      </fieldset>

      <ErrorText>{error}</ErrorText>
      <div className="flex gap-2">
        <Button variant="ghost" className="flex-1" disabled={busy} onClick={() => answer(false)}>Cancel</Button>
        <Button className="flex-1" disabled={busy || !ws || (!!chosen?.brands && !brandFor(ws))} onClick={() => answer(true)}>{busy ? "Connecting…" : `Allow ${appName}`}</Button>
      </div>
      <p className="text-center text-[12px] text-muted-foreground">
        You'll go back to <span className="font-mono">{info.redirect_host}</span>. Change this or disconnect any time under Connect your AI.
        <br />
        Signed in as {email}. <button type="button" onClick={onSignOut} className="underline">Not you?</button>
      </p>
    </CenteredForm>
  );
}
