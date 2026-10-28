import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { ImportResult, Mailbox, MailboxHealth, MailboxHealthSummary, MailboxStatus, Sender, SendingDomain } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { BrandBadge } from "@/components/brand-badge";
import { api, errorMessage } from "../api";
import { ask, Badge, Button, Card, confirmAction, Empty, ErrorText, field, FilePicker, Loading, Menu, PageHeader, Stat, Stats, toast } from "../ui";
import { OAuthConnect } from "./OAuth";
import type { Tab } from "../App";
import { placementTone, plural, shortPlatform } from "../format";
import { DataTable } from "@/components/data-table";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { mailboxStatus } from "@/lib/status";

// Lifecycle stage: only ready mailboxes send cold email.
const stageTone = { warming: "blue", ready: "green", resting: "grey", retired: "grey" } as const;

export function Mailboxes({ ws, canManage, openOAuth, go }: { ws: string; canManage: boolean; openOAuth?: boolean; go: (tab: Tab, item?: string) => void }) {
  const [mailboxes, setMailboxes] = useState<Mailbox[]>([]);
  const [domains, setDomains] = useState<SendingDomain[]>([]);
  const [senders, setSenders] = useState<Sender[]>([]);
  const [health, setHealth] = useState<{ summary: MailboxHealthSummary; byId: Map<string, MailboxHealth> } | null>(null);
  const [healthOpen, setHealthOpen] = useState<MailboxHealth | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [adding, setAdding] = useState<boolean | null>(openOAuth ? true : null);
  const [filter, setFilter] = useState<"" | MailboxStatus>("");
  // Connected sequencers that can run warm-up themselves.
  const [warmupPlatforms, setWarmupPlatforms] = useState<{ id: string; name: string; config: Record<string, unknown> }[]>([]);

  const brand = useBrand(ws).brand?.handle;
  const load = useCallback(async () => {
    const path = { params: { path: { ws } } };
    const [m, d, s, h] = await Promise.all([api.GET("/v1/workspaces/{ws}/mailboxes", { params: { path: { ws }, query: { ...(brand ? { brand } : {}) } } }), api.GET("/v1/workspaces/{ws}/sending-domains", { params: { path: { ws }, query: { ...(brand ? { brand } : {}) } } }), api.GET("/v1/workspaces/{ws}/senders", path), api.GET("/v1/workspaces/{ws}/mailbox-health", { params: { path: { ws }, query: { ...(brand ? { brand } : {}) } } })]);
    setMailboxes(m.data?.data ?? []);
    setDomains(d.data?.data ?? []);
    setSenders(s.data?.data ?? []);
    if (h.data) setHealth({ summary: h.data.summary, byId: new Map(h.data.data.map((x) => [x.mailbox_id, x])) });
    const i = await api.GET("/v1/workspaces/{ws}/integrations", { params: { path: { ws }, query: { ...(brand ? { brand } : {}) } } });
    setWarmupPlatforms((i.data?.data ?? []).filter((x) => x.capabilities.includes("control.warmup")).map((x) => ({ id: x.id, name: x.name, config: x.config })));
    setLoaded(true);
  }, [ws, brand]);

  useEffect(() => {
    load();
  }, [load]);

  // Connection checks run in the background; refresh until they settle.
  const pending = mailboxes.some((m) => m.status === "verifying");
  useEffect(() => {
    if (!pending) return;
    const t = setInterval(load, 2000);
    return () => clearInterval(t);
  }, [pending, load]);

  const counts = mailboxes.reduce<Record<string, number>>((acc, m) => ({ ...acc, [m.status]: (acc[m.status] ?? 0) + 1 }), {});
  // Domains hold their mailboxes to the domain's cap, and to 0 while paused.
  const capacity = domains.length > 0 ? domains.reduce((sum, d) => sum + d.daily_capacity, 0) : mailboxes.filter((m) => m.status === "active").reduce((sum, m) => sum + m.daily_limit, 0);
  const needAttention = domains.filter((d) => d.health === "warning" || d.health === "critical").length;
  const pausedDomains = new Set(domains.filter((d) => d.status === "paused").map((d) => d.domain));
  // With no mailboxes yet, the ways to connect them are the page.
  const showAdd = canManage && (adding ?? (loaded && mailboxes.length === 0));
  const shown = filter ? mailboxes.filter((m) => m.status === filter) : mailboxes;
  // The connected sequencers a mailbox is in that can warm it up.
  // Outflare doesn't send warm-up mail itself.
  const platformsFor = (m: Mailbox) => warmupPlatforms.filter((p) => (m.platforms ?? []).some((x) => x.integration_id === p.id));
  const warming = mailboxes.filter((m) => m.warmup.enabled);
  const notWarming = mailboxes.filter((m) => !m.warmup.enabled && platformsFor(m).length > 0);
  const scores = warming.flatMap((m) => (m.warmup.platform?.score == null ? [] : [m.warmup.platform.score]));
  const score = scores.length > 0 ? scores.reduce((a, b) => a + b, 0) / scores.length : null;

  async function setWarmup(list: Mailbox[], enabled: boolean) {
    if (await changeWarmup(ws, list, enabled)) load();
  }

  // Which sequencer runs warm-up for these mailboxes. Turns it on too.
  async function setProvider(list: Mailbox[], provider: string) {
    const name = warmupPlatforms.find((p) => p.id === provider)?.name ?? "the sequencer";
    const { data, error } = await api.POST("/v1/workspaces/{ws}/mailboxes/warmup", { params: { path: { ws } }, body: { enabled: true, provider, mailbox_ids: list.map((m) => m.id) } });
    if (error) return toast(errorMessage(error), "error");
    toast(`${plural(data?.updated ?? 0, "mailbox", "mailboxes")} warm${data?.updated === 1 ? "s" : ""} up in ${name}.`);
    load();
  }

  async function chooseProvider(m: Mailbox) {
    const r = await ask({
      title: `Warm up ${m.email} in`,
      body: <p className="text-sm">It's in more than one sequencer that can warm it up. Outflare turns warm-up on in the one you pick and off in the other, and shows its warm-up score.</p>,
      choices: [
        { label: "Cancel", value: "cancel", variant: "ghost" },
        ...platformsFor(m).map((p) => ({ label: p.name, value: p.id, variant: m.warmup.provider === p.id ? ("primary" as const) : undefined })),
      ],
    });
    if (!r || r.choice === "cancel") return;
    setProvider([m], r.choice);
  }

  async function setSender(m: Mailbox, senderId: string) {
    const { error } = await api.POST("/v1/workspaces/{ws}/mailboxes/sender", { params: { path: { ws } }, body: { mailbox_ids: [m.id], sender_id: senderId || null } });
    if (error) return toast(errorMessage(error), "error");
    toast(senderId ? `${m.email} sends as ${senders.find((x) => x.id === senderId)?.name}.` : `Cleared the sender for ${m.email}.`);
    load();
  }

  async function allocate(m: Mailbox, action: "release" | "manual_only" | "unlock") {
    const path = { params: { path: { ws } } };
    const { error } =
      action === "release"
        ? await api.POST("/v1/workspaces/{ws}/mailboxes/release", { ...path, body: { mailbox_ids: [m.id] } })
        : await api.POST("/v1/workspaces/{ws}/mailboxes/lock", { ...path, body: { mailbox_ids: [m.id], kind: action === "manual_only" ? "manual_only" : null } });
    if (error) return toast(errorMessage(error), "error");
    toast(action === "release" ? `${m.email} is free for other sequences${m.assignment?.state === "active" ? " once its follow-ups are done" : ""}.` : action === "manual_only" ? `${m.email} is only moved by hand now.` : `${m.email} can be allocated automatically again.`);
    load();
  }

  async function act(m: Mailbox, action: "pause" | "resume" | "verify" | "delete" | "test" | "sync") {
    const params = { params: { path: { ws, id: m.id } } };
    if (action === "test") {
      const r = await ask({
        title: "Send a test email",
        body: <p>Sends one email from <b className="font-mono">{m.email}</b> now, so you can check it arrives and how it looks. It goes through the suppression list like any other email.</p>,
        input: { label: "Send to", type: "email", placeholder: "you@yourcompany-com.example" },
        choices: [{ label: "Cancel", value: "cancel" }, { label: "Send test", value: "send", variant: "primary" }],
      });
      if (!r) return;
      const { error } = await api.POST("/v1/workspaces/{ws}/mailboxes/{id}/test-send", { ...params, body: { to: r.value } });
      return error ? toast(errorMessage(error), "error") : toast(`Test email sent to ${r.value}.`);
    }
    if (action === "delete") {
      const ok = await confirmAction(`Remove ${m.email}?`, <p>It stops sending, and its stored {m.auth_method === "oauth" ? "sign-in" : "password"} is deleted. Leads it was emailing move to your other mailboxes. Sent emails stay in the message log.</p>, "Remove mailbox");
      if (!ok) return;
      await api.DELETE("/v1/workspaces/{ws}/mailboxes/{id}", params);
      toast(`Removed ${m.email}.`);
    } else if (action === "sync") {
      await api.POST("/v1/workspaces/{ws}/mailboxes/{id}/sync", params);
      toast(`Checking ${m.email} for new mail.`);
    } else if (action === "verify") {
      await api.POST("/v1/workspaces/{ws}/mailboxes/{id}/verify", params);
      toast(`Checking ${m.email} can sign in.`);
    } else {
      await api.PATCH("/v1/workspaces/{ws}/mailboxes/{id}", { ...params, body: { status: action === "pause" ? "paused" : "active" } });
      toast(action === "pause" ? `Paused ${m.email}. It sends nothing until resumed.` : `Resumed ${m.email}.`);
    }
    load();
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title="Mailboxes"
        description="The inboxes your sequences send from. Each one sends up to its daily limit, and replies to it land in the Inbox."
        action={canManage && mailboxes.length > 0 && <Button variant={showAdd ? "ghost" : "primary"} onClick={() => setAdding(!showAdd)}>{showAdd ? "Close" : "Add mailboxes"}</Button>}
      />

      {mailboxes.length > 0 && (
        <Stats cols={5}>
          <Stat label="Mailboxes" value={mailboxes.length} />
          {/* Stage counts, the same numbers Infrastructure and Home show. */}
          <Stat label="Ready" value={mailboxes.filter((m) => m.lifecycle.stage === "ready").length} sub={`${mailboxes.filter((m) => m.lifecycle.stage === "warming").length} warming up · ${mailboxes.filter((m) => m.lifecycle.stage === "ready" && m.status === "paused").length} paused · ${mailboxes.filter((m) => m.assignment).length} on a sequence`} />
          {health ? (
            <Stat label="Need attention" value={health.summary.critical + health.summary.warning} tone={health.summary.critical > 0 ? "red" : health.summary.warning > 0 ? "amber" : undefined} sub={health.summary.critical > 0 ? `${health.summary.critical} critical` : health.summary.paused > 0 ? `${health.summary.paused} paused` : "all healthy"} />
          ) : (
            <Stat label="Need attention" value={counts.error ?? 0} tone={(counts.error ?? 0) > 0 ? "red" : undefined} />
          )}
          <Stat label="Capacity / day" value={capacity.toLocaleString()} sub="held to each domain's cap" />
          <div className="col-span-2 sm:col-span-1">
          <Stat
            label="Warm-up score"
            value={score === null ? "–" : Math.round(score)}
            tone={placementTone(score === null ? null : score / 100)}
            sub={score === null ? "from your sequencer's warm-up" : `average of ${plural(scores.length, "warming mailbox", "warming mailboxes")}`}
          />
          </div>
        </Stats>
      )}

      {canManage && loaded && mailboxes.length > 0 && warming.length === 0 && (
        <Card
          title="Warm up your mailboxes before sending"
          description="New inboxes that send cold email straight away land in spam. Warm-up builds their reputation first."
          action={
            notWarming.length > 0 ? (
              <Button onClick={() => setWarmup(notWarming, true)}>{notWarming.length === 1 ? "Warm up 1 mailbox" : `Warm up ${notWarming.length} mailboxes`}</Button>
            ) : (
              warmupPlatforms.length === 0 && <Button onClick={() => go("connectors", "sequencer")}>Connect a sequencer</Button>
            )
          }
        >
          <p className="text-sm text-muted-foreground">
            Warm-up runs in a sequencer with its own warm-up: EmailBison, Instantly or Smartlead. Connect one and add your mailboxes there; Outflare turns its warm-up on and shows the score. Plan on two weeks before a new mailbox sends sequences.
          </p>
        </Card>
      )}

      {canManage && loaded && warmupPlatforms.map((p) => {
        const there = mailboxes.filter((m) => (m.platforms ?? []).some((x) => x.integration_id === p.id));
        const using = there.filter((m) => m.warmup.enabled && m.warmup.provider === p.id);
        if (there.length === 0) return null;
        return (
          <Card
            key={p.id}
            title={`Warm-up for your ${p.name} mailboxes`}
            action={
              <div className="flex flex-wrap gap-2">
                {using.length < there.length && <Button onClick={() => setProvider(there, p.id)}>Warm up {there.length === 1 ? "it" : `all ${there.length}`} in {p.name}</Button>}
              </div>
            }
          >
            <p className="text-sm text-muted-foreground">
              {using.length === there.length
                ? `All ${plural(there.length, "mailbox", "mailboxes")} warm up in ${p.name}. Outflare shows ${p.name}'s warm-up score.`
                : `${plural(there.length, "mailbox is", "mailboxes are")} in ${p.name}${using.length > 0 ? `, and ${using.length} warm${using.length === 1 ? "s" : ""} up there` : ""}. Turn on ${p.name}'s warm-up for all of them here, or for one from its menu.`}
            </p>
            {using.some((m) => m.has_login !== false) && <WarmupTags ws={ws} platform={p} onSaved={load} />}
          </Card>
        );
      })}

      {showAdd && (
        <div className="space-y-6">
          <Import ws={ws} onDone={load} />
          <OAuthConnect ws={ws} onDone={load} />
        </div>
      )}

      {!loaded && <Loading />}
      {loaded && mailboxes.length === 0 && !canManage && <Empty title="No mailboxes yet">An admin or operator connects them.</Empty>}

      {mailboxes.length > 0 && (
        <DataTable
          data={shown}
          rowId={(m) => m.id}
          searchPlaceholder="Search mailboxes, senders or sequences"
          hidden={["pool", "stage", "health"]}
          empty="No mailboxes match."
          toolbar={
            <>
              <select aria-label="Show" className={field} value={filter} onChange={(e) => setFilter(e.target.value as typeof filter)}>
                <option value="">All ({mailboxes.length})</option>
                {(["active", "error", "paused", "verifying"] as MailboxStatus[]).map((st) => counts[st] ? <option key={st} value={st}>{statusLabel[st]} ({counts[st]})</option> : null)}
              </select>
              {canManage && warming.length > 0 && notWarming.length > 0 && <Button variant="ghost" onClick={() => setWarmup(notWarming, true)}>Warm up the other {notWarming.length}</Button>}
            </>
          }
          mobile={(m) => (
            <div className="space-y-1.5">
              <div className="flex items-start justify-between gap-2">
                <MailboxName m={m} go={go} />
                <StatusCell m={m} paused={pausedDomains.has(m.domain)} h={health?.byId.get(m.id!)} />
              </div>
              <div className="flex items-center justify-between gap-2 text-xs text-muted-foreground">
                <span>{m.assignment ? m.assignment.sequence_name : "Free"} · {m.daily_limit}/day</span>
                {canManage && <Actions m={m} act={act} setWarmup={setWarmup} allocate={allocate} choose={() => chooseProvider(m)} sequencers={platformsFor(m).length} />}
              </div>
            </div>
          )}
          columns={[
            { id: "email", header: "Mailbox", accessorFn: (m) => `${m.email} ${m.domain}`, enableHiding: false, cell: ({ row: { original: m } }) => <MailboxName m={m} go={go} /> },
            { id: "status", header: "Status", accessorFn: (m) => statusLabel[m.status], cell: ({ row: { original: m } }) => <StatusCell m={m} paused={pausedDomains.has(m.domain)} h={health?.byId.get(m.id!)} /> },
            {
              id: "stage",
              header: "Stage",
              accessorFn: (m) => m.lifecycle.stage,
              cell: ({ row: { original: m } }) => (
                <button onClick={(e) => { e.stopPropagation(); go("infrastructure"); }} title={m.lifecycle.reason || "Only ready mailboxes send cold email. Open Infrastructure to change it."}>
                  <Badge tone={stageTone[m.lifecycle.stage]}>{m.lifecycle.stage[0].toUpperCase() + m.lifecycle.stage.slice(1)}</Badge>
                </button>
              ),
            },
            {
              id: "health",
              header: "Health",
              accessorFn: (m) => health?.byId.get(m.id!)?.score ?? 101,
              cell: ({ row: { original: m } }) => {
                const h = health?.byId.get(m.id!);
                return h ? <HealthCell h={h} onOpen={() => setHealthOpen(h)} /> : <span className="text-muted-foreground">–</span>;
              },
            },
            {
              id: "warmup",
              header: "Warm-up",
              accessorFn: (m) => (m.warmup.enabled ? (m.warmup.platform?.score ?? 0) : -1),
              cell: ({ row: { original: m } }) => <WarmupCell m={m} />,
            },
            {
              id: "sender",
              header: "Sends as",
              accessorFn: (m) => m.sender?.name ?? "",
              cell: ({ row: { original: m } }) =>
                canManage && senders.length > 0 ? (
                  <select aria-label={`Sender for ${m.email}`} className={`${field} max-w-44 ${m.sender?.confirmed ? "" : "text-amber-700 dark:text-amber-400"}`} value={m.sender?.confirmed ? m.sender.id : ""} onChange={(e) => setSender(m, e.target.value)}>
                    <option value="">{m.suggested_sender_id ? "Confirm…" : "Not confirmed"}</option>
                    {senders.filter((x) => x.status === "active" || x.id === m.sender?.id).map((x) => <option key={x.id} value={x.id}>{x.name}{x.id === m.suggested_sender_id ? " (suggested)" : ""}</option>)}
                  </select>
                ) : (
                  m.sender?.confirmed ? m.sender.name : <span className="text-muted-foreground">Not confirmed</span>
                ),
            },
            {
              id: "sequence",
              header: "Sending for",
              accessorFn: (m) => m.assignment?.sequence_name ?? "",
              cell: ({ row: { original: m } }) => (
                <div className="flex flex-col items-start gap-1">
                  {m.assignment ? (
                    <button className="text-left underline decoration-border underline-offset-2 hover:decoration-foreground" title={m.assignment.reason || undefined} onClick={() => go("sequences", m.assignment!.sequence_id)}>{m.assignment.sequence_name}</button>
                  ) : (
                    <span className="text-muted-foreground">Free</span>
                  )}
                  {m.assignment?.platform && <Badge tone={m.assignment.confirmed ? "grey" : "blue"} title={m.assignment.confirmed ? undefined : "Asked the platform to add it; confirmed on the next read, within 5 minutes."}>{m.assignment.confirmed ? `in ${shortPlatform(m.assignment.platform)}` : `waiting for ${shortPlatform(m.assignment.platform)}`}</Badge>}
                  {m.assignment?.state === "draining" && <Badge tone="amber" title="Takes no new leads; finishing the follow-ups it started">Finishing follow-ups</Badge>}
                  {m.lock && <Badge title={m.lock.note || undefined}>{m.lock.kind === "manual_only" ? "Manual only" : m.lock.kind === "team" ? "Team only" : "Kept here"}</Badge>}
                </div>
              ),
            },
            { id: "pool", header: "Pool", accessorFn: (m) => m.pool_name ?? "", cell: ({ row: { original: m } }) => m.pool_name ?? <span className="text-muted-foreground">None</span> },
            { id: "limit", header: "Daily limit", accessorFn: (m) => m.daily_limit, meta: { align: "right", mono: true } },
            {
              id: "actions",
              header: () => <span className="sr-only">Actions</span>,
              enableSorting: false,
              enableHiding: false,
              meta: { align: "right" },
              cell: ({ row: { original: m } }) => canManage && <Actions m={m} act={act} setWarmup={setWarmup} allocate={allocate} choose={() => chooseProvider(m)} sequencers={platformsFor(m).length} />,
            },
          ]}
        />
      )}

      {domains.length > 0 && (
        <p className="text-sm text-muted-foreground">
          Sending from {domains.length} domain{domains.length === 1 ? "" : "s"}
          {needAttention > 0 && <span className="text-amber-700 dark:text-amber-400">, {needAttention} need{needAttention === 1 ? "s" : ""} attention</span>}
          {pausedDomains.size > 0 && <>, {pausedDomains.size} paused</>}.{" "}
          <button className="font-medium underline underline-offset-2" onClick={() => go("domains")}>DNS health, limits and pausing are under Domains →</button>
        </p>
      )}
      {healthOpen && <HealthDialog h={healthOpen} onClose={() => setHealthOpen(null)} />}
    </div>
  );
}

// Turns warm-up on (after saying what it does) or off for some mailboxes,
// in the connected sequencer each one is in. True once it changed.
export async function changeWarmup(ws: string, list: Mailbox[], enabled: boolean): Promise<boolean> {
  const many = list.length > 1;
  if (enabled) {
    const ok = await confirmAction(
      many ? `Warm up ${list.length} mailboxes?` : `Warm up ${list[0].email}?`,
      <div className="space-y-2">
        <p>Outflare turns on the warm-up of the sequencer {many ? "each mailbox is" : "it's"} in (EmailBison, Instantly or Smartlead). Its warm-up network emails the mailbox, opens the mail, moves it out of spam and replies, which tells Google and Microsoft people want mail from it.</p>
        <p>Outflare shows the sequencer's warm-up score here, and leaves its warm-up mail out of your Inbox when you add its warm-up tag.</p>
      </div>,
      "Start warm-up",
      false,
    );
    if (!ok) return false;
  }
  const { data, error } = await api.POST("/v1/workspaces/{ws}/mailboxes/warmup", { params: { path: { ws } }, body: { enabled, mailbox_ids: list.map((m) => m.id) } });
  if (error) {
    toast(errorMessage(error), "error");
    return false;
  }
  toast(enabled ? `Warm-up is on for ${plural(data?.updated ?? 0, "mailbox", "mailboxes")}.` : `Warm-up is off for ${plural(data?.updated ?? 0, "mailbox", "mailboxes")}.`);
  return true;
}

export function WarmupCell({ m }: { m: Mailbox }) {
  const w = m.warmup;
  // Sequencers whose own warm-up is on for it, besides the one running it.
  const elsewhere = (m.platforms ?? []).filter((p) => p.warmup_enabled && p.integration_id !== w.platform?.integration_id).map((p) => shortPlatform(p.platform));
  if (!w.enabled || !w.platform)
    return elsewhere.length > 0 ? (
      <Badge tone="blue" title={`${elsewhere.join(" and ")}'s own warm-up is on for this mailbox.`}>On in {elsewhere.join(", ")}</Badge>
    ) : (
      <span className="text-muted-foreground">Off</span>
    );
  const score = w.platform.score;
  return (
    <div>
      {w.waiting_on ? (
        <Badge tone="blue" title="Outflare asked the sequencer to turn its warm-up on. It shows here after the next sync, within 5 minutes.">Turning on in {w.platform.name}</Badge>
      ) : (
        <Badge tone={placementTone(score === null ? null : score / 100)} title={`${w.platform.name} runs this mailbox's warm-up${score === null ? "" : `; its warm-up score is ${score}`}`}>{score === null ? "On" : `${score}`} in {w.platform.name}</Badge>
      )}
      {elsewhere.length > 0 && (
        <div className="mt-0.5"><Badge tone="amber" title="It warms up in two places, which doubles its warm-up mail. Choose one with Warm up in… in its menu.">Also warming in {elsewhere.join(", ")}</Badge></div>
      )}
    </div>
  );
}

const statusLabel = Object.fromEntries(Object.entries(mailboxStatus).map(([k, v]) => [k, v.label])) as Record<MailboxStatus, string>;
type Act = (m: Mailbox, action: "pause" | "resume" | "verify" | "delete" | "test" | "sync") => void;

function MailboxName({ m, go }: { m: Mailbox; go: (tab: Tab, item?: string) => void }) {
  return (
    <div className="min-w-0 sm:min-w-[16rem]">
      <div className="flex flex-wrap items-center gap-1.5"><span className="font-mono text-[13px] font-medium break-all">{m.email}</span> <BrandBadge id={m.brand_id} /></div>
      <div className="text-xs text-muted-foreground">
        {m.has_login === false
          ? <span title="Outflare has no login for it, so it only sends in that platform's campaigns and has no inbox sync here.">In {(m.platforms ?? []).map((p) => shortPlatform(p.platform)).join(", ") || "an outside sequencer"}</span>
          : <>{providerLabel(m.provider)}{m.auth_method === "oauth" ? " · signed in with OAuth" : " · app password"}</>} ·{" "}
        <button className="underline-offset-2 hover:underline" title={`DNS health and settings for ${m.domain}`} onClick={() => go("domains", m.domain)}>{m.domain}</button>
      </div>
      {m.has_login !== false && (m.platforms ?? []).length > 0 && (
        <div className="mt-1 text-xs text-amber-700 dark:text-amber-400" title="Sends there count against the same inbox and domain reputation. Check its limits there too.">
          Also connected in {(m.platforms ?? []).map((p) => shortPlatform(p.platform)).join(", ")}, which may send from it too
        </div>
      )}
      {m.status === "error" && m.status_reason && <Problem reason={m.status_reason} />}
      {m.sync_error && <div className="mt-1 max-w-md text-xs text-amber-700 dark:text-amber-400">Can't read new mail: {m.sync_error}</div>}
    </div>
  );
}

// One status per mailbox: the most serious of its sign-in state, its
// domain, its stage and its health, with the reason underneath. Stage and
// Health have their own columns, hidden until wanted.
function StatusCell({ m, paused, h }: { m: Mailbox; paused: boolean; h?: MailboxHealth }) {
  const problem = h?.signals.find((s) => s.level === "critical") ?? h?.signals.find((s) => s.level === "warning");
  const stage = m.lifecycle.stage;
  const [label, tone, reason] =
    m.status !== "active" ? [mailboxStatus[m.status].label, mailboxStatus[m.status].tone, m.status === "paused" ? m.lifecycle.reason || undefined : undefined]
    : paused ? ["Paused", "grey" as const, "The whole domain is paused, under Domains"]
    : stage === "warming" ? ["Warming up", "blue" as const, m.lifecycle.reason || "No cold email until warm-up ends"]
    : stage === "resting" ? ["Resting", "grey" as const, m.lifecycle.reason || undefined]
    : stage === "retired" ? ["Retired", "grey" as const, undefined]
    : problem ? ["Needs attention", problem.level === "critical" ? "red" as const : "amber" as const, problem.title]
    : [mailboxStatus.active.label, mailboxStatus.active.tone, undefined];
  return (
    <div className="flex max-w-56 flex-col items-start gap-1">
      <Badge tone={tone}>{label}</Badge>
      {reason && <span className="text-xs text-muted-foreground">{reason}</span>}
      {(m.platforms ?? []).filter((p) => p.status !== "ok").map((p) => (
        <Badge key={p.integration_id} tone={p.status === "paused" ? "grey" : "red"} title={p.status_reason ?? undefined}>{p.status} in {shortPlatform(p.platform)}</Badge>
      ))}
    </div>
  );
}

function Actions({ m, act, setWarmup, allocate, choose, sequencers }: { m: Mailbox; act: Act; setWarmup: (list: Mailbox[], enabled: boolean) => void; allocate: (m: Mailbox, a: "release" | "manual_only" | "unlock") => void; choose?: () => void; sequencers: number }) {
  // Mailboxes known only from an outside sequencer have no login here.
  // Warm-up runs in a connected sequencer the mailbox is in.
  const login = m.has_login !== false;
  const canWarm = sequencers > 0 || m.warmup.enabled;
  return (
    <div className="flex justify-end gap-2 whitespace-nowrap">
      {m.status === "error" && login ? (
        <Button variant="ghost" onClick={() => act(m, "verify")}>Try again</Button>
      ) : m.status === "paused" ? (
        <Button variant="ghost" onClick={() => act(m, "resume")}>Resume</Button>
      ) : (
        m.status === "active" && <Button variant="ghost" onClick={() => act(m, "pause")}>Pause</Button>
      )}
      <Menu
        items={[
          canWarm && { label: m.warmup.enabled ? "Turn off warm-up" : "Turn on warm-up", onClick: () => setWarmup([m], !m.warmup.enabled) },
          sequencers > 1 && !!choose && { label: "Warm up in…", onClick: choose },
          login && m.status === "active" && { label: "Send a test email", onClick: () => act(m, "test") },
          login && m.status === "active" && { label: "Check for new mail", onClick: () => act(m, "sync") },
          login && m.status !== "error" && m.status !== "verifying" && { label: "Re-check sign-in", onClick: () => act(m, "verify") },
          m.status === "error" && { label: "Pause", onClick: () => act(m, "pause") },
          !!m.assignment && { label: `Free from ${m.assignment.sequence_name}`, onClick: () => allocate(m, "release") },
          m.lock?.kind === "manual_only" ? { label: "Allow automatic allocation", onClick: () => allocate(m, "unlock") } : { label: "Only move by hand", onClick: () => allocate(m, "manual_only") },
          { label: "Remove", onClick: () => act(m, "delete"), danger: true },
        ]}
      />
    </div>
  );
}


function providerLabel(p: string) {
  return ({ google: "Google Workspace", microsoft: "Microsoft 365", other: "Other provider" } as Record<string, string>)[p] ?? p;
}

// Turns a raw SMTP or IMAP error into what to do about it, keeping the
// original underneath for whoever debugs it.
function Problem({ reason }: { reason: string }) {
  const r = reason.toLowerCase();
  const hint =
    r.includes("535") || r.includes("username and password") || r.includes("authentication failed") || r.includes("invalid credentials")
      ? "The password was rejected. Google Workspace and Microsoft 365 need an app password (not the account password), with IMAP and SMTP turned on for the mailbox."
      : r.includes("timeout") || r.includes("i/o timeout") || r.includes("no such host") || r.includes("connection refused")
        ? "Couldn't reach the mail server. Check the host and port in your CSV, or try again in a few minutes."
        : r.includes("certificate") || r.includes("tls")
          ? "The mail server's secure connection failed. Check the port: 465 and 993 use TLS from the start, 587 and 143 upgrade with STARTTLS."
          : r.includes("oauth") || r.includes("token") || r.includes("consent")
            ? "The Google or Microsoft sign-in stopped working. Connect the mailbox again, or check the admin connection's permissions."
            : null;
  return (
    <div className="mt-1 max-w-md text-xs">
      {hint && <p className="text-red-700 dark:text-red-300">{hint}</p>}
      <p className="whitespace-pre-line font-mono text-red-600/80 dark:text-red-400/80">{reason}</p>
    </div>
  );
}


function Import({ ws, onDone }: { ws: string; onDone: () => void }) {
  const [file, setFile] = useState<File | null>(null);
  const [pool, setPool] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<ImportResult | null>(null);
  const [error, setError] = useState("");

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!file) return;
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/mailboxes/import", {
      params: { path: { ws }, query: pool ? { pool } : {} },
      body: await file.text(),
      bodySerializer: (b: string) => b,
      headers: { "Content-Type": "text/csv" },
    });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    setError("");
    setResult(data ?? null);
    setFile(null);
    onDone();
  }

  return (
    <Card title="Import from a CSV" description="The fastest way to connect hundreds of reseller inboxes at once.">
      <form onSubmit={submit} className="space-y-4 text-sm">
        <p className="text-muted-foreground">
          Use the CSV your inbox provider gives you. It needs <code>email</code> and <code>password</code> columns, where the password is an app password. Google Workspace and Microsoft 365 are detected automatically. Other providers also need <code>smtp_host</code>, <code>smtp_port</code>, <code>imap_host</code> and <code>imap_port</code>. Importing a mailbox that's already here updates it.
        </p>
        <FilePicker file={file} onChange={setFile} accept=".csv,text/csv" example={{ name: "mailboxes-example.csv", content: "email,password,display_name,daily_limit\nana@yourdomain-com.example,abcd efgh ijkl mnop,Ana Smith,30\n" }} />
        <div className="flex flex-wrap items-end gap-3">
          <label className="block">
            <span className="mb-1 block font-medium">Pool <span className="font-normal text-muted-foreground">(optional)</span></span>
            <input className={field} placeholder="e.g. Warm" value={pool} onChange={(e) => setPool(e.target.value)} />
            <span className="mt-1 block text-xs text-muted-foreground">Group mailboxes so a sequence can send from just this group.</span>
          </label>
          <Button disabled={!file || busy} title={!file ? "Choose a CSV file first" : undefined}>{busy ? "Importing…" : "Import mailboxes"}</Button>
        </div>
        <ErrorText>{error}</ErrorText>
        {result && (
          <div className="rounded bg-muted p-3">
            <p>
              {result.created} added, {result.updated} updated{result.errors.length > 0 && `, ${result.errors.length} skipped`}. We're now signing in to each one over SMTP and IMAP; their status updates below in a few seconds.
            </p>
            {result.errors.length > 0 && (
              <ul className="mt-2 space-y-1 text-red-600 dark:text-red-400">
                {result.errors.map((e) => <li key={`${e.line}-${e.email}`}>Line {e.line}{e.email ? ` (${e.email})` : ""}: {e.error}</li>)}
              </ul>
            )}
          </div>
        )}
      </form>
    </Card>
  );
}

const levelTone = { ok: "green", warning: "amber", critical: "red", paused: "grey" } as const;
const levelLabel = { ok: "Healthy", warning: "Warning", critical: "Critical", paused: "Paused" } as const;

// A mailbox's health grade, opening the reasons and fixes.
function HealthCell({ h, onOpen }: { h: MailboxHealth; onOpen: () => void }) {
  const problems = h.signals.filter((s) => s.level === "warning" || s.level === "critical");
  const dot = { ok: "bg-green-500", warning: "bg-amber-500", critical: "bg-red-500", paused: "bg-muted-foreground/60" }[h.level];
  return (
    <button type="button" onClick={onOpen} className="group inline-flex max-w-56 items-center gap-1.5 text-left" title={problems.map((s) => s.title).join("\n") || "No problems found"}>
      <span aria-hidden="true" className={`size-1.5 shrink-0 rounded-full ${dot}`} />
      <span className="truncate group-hover:underline">{problems[0]?.title ?? levelLabel[h.level]}</span>
      {problems.length > 1 && <span className="shrink-0 font-mono text-[11px] text-muted-foreground">+{problems.length - 1}</span>}
    </button>
  );
}

function HealthDialog({ h, onClose }: { h: MailboxHealth; onClose: () => void }) {
  const st = h.stats;
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader className="text-left">
          <DialogTitle className="flex items-center gap-2 text-[15px]">{h.email}<Badge tone={levelTone[h.level]}>{levelLabel[h.level]} · {h.score}</Badge></DialogTitle>
          <DialogDescription className="text-[13px]">
            {st.sent_7d.toLocaleString()} sent this week · {st.bounce_rate_7d === null ? `${st.bounced_7d} bounced` : `${st.bounce_rate_7d}% bounced`} · {st.replies_7d} replies
            {st.inbox_placement_7d !== null && ` · ${Math.round(st.inbox_placement_7d)}% inbox placement`}
          </DialogDescription>
        </DialogHeader>
        {h.signals.length === 0 ? (
          <p className="text-[13px] text-muted-foreground">No problems found: it signs in, replies sync, bounces are low and the domain checks pass.</p>
        ) : (
          <ul className="space-y-3">
            {h.signals.map((s) => (
              <li key={s.key} className="rounded-lg border p-3 text-[13px]">
                <div className="flex items-center gap-2 font-medium"><Badge tone={levelTone[s.level]}>{s.level}</Badge>{s.title}</div>
                {s.detail && <p className="mt-1 break-words text-[12px] text-muted-foreground">{s.detail}</p>}
                {s.fix && <p className="mt-1.5"><span className="font-medium">What to do: </span>{s.fix}</p>}
              </li>
            ))}
          </ul>
        )}
      </DialogContent>
    </Dialog>
  );
}

// WarmupTags keeps the tag a sequencer puts in its warm-up mail, so the
// Outflare inbox can leave that mail out.
function WarmupTags({ ws, platform, onSaved }: { ws: string; platform: { id: string; name: string; config: Record<string, unknown> }; onSaved: () => void }) {
  const saved = Array.isArray(platform.config.warmup_tags) ? (platform.config.warmup_tags as string[]) : [];
  const [value, setValue] = useState(saved.join(", "));
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    const tags = value.split(",").map((t) => t.trim()).filter(Boolean);
    const { error } = await api.PATCH("/v1/workspaces/{ws}/integrations/{id}", { params: { path: { ws, id: platform.id } }, body: { config: { ...platform.config, warmup_tags: tags } } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast(tags.length > 0 ? `${platform.name} warm-up mail will stay out of your Outflare inbox` : "Warm-up tag removed");
    onSaved();
  };
  return (
    <form onSubmit={save} className="mt-3 flex flex-col gap-1.5 border-t pt-3">
      <label htmlFor={`tag-${platform.id}`} className="text-sm font-medium">{platform.name} warm-up tag</label>
      <p className="text-sm text-muted-foreground">
        {platform.name} adds a short code to each warm-up email. Paste it from {platform.name}'s warm-up settings and Outflare's inbox will leave that mail out, without opening or moving it, so {platform.name} can still do its part.
      </p>
      <div className="flex flex-wrap gap-2">
        <input id={`tag-${platform.id}`} className={`${field} w-56`} placeholder="e.g. XK7Q2Z9" value={value} onChange={(e) => setValue(e.target.value)} />
        <Button type="submit" variant="ghost" disabled={busy || value === saved.join(", ")}>Save</Button>
      </div>
      <ErrorText>{error}</ErrorText>
    </form>
  );
}
