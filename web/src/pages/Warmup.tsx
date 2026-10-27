import { useCallback, useEffect, useState } from "react";
import type { Mailbox } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, Card, Empty, ErrorText, Loading, PageHeader, Stat, Stats } from "../ui";
import type { Tab } from "../App";
import { placementTone } from "../format";
import { DataTable } from "@/components/data-table";
import { changeWarmup, WarmupCell } from "./Mailboxes";

// Warm-up across every mailbox. Outflare doesn't send warm-up mail itself:
// a connected sequencer with its own warm-up (EmailBison, Instantly,
// Smartlead) runs it, and Outflare switches it on there and shows its
// score. The same switches are in each mailbox's menu on the Mailboxes page.
export function Warmup({ ws, canManage, go }: { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void }) {
  const [mailboxes, setMailboxes] = useState<Mailbox[] | null>(null);
  const [sequencers, setSequencers] = useState<{ id: string; name: string }[]>([]);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    const path = { params: { path: { ws } } };
    const [m, i] = await Promise.all([api.GET("/v1/workspaces/{ws}/mailboxes", path), api.GET("/v1/workspaces/{ws}/integrations", path)]);
    if (m.error || !m.data) return setError(errorMessage(m.error));
    setMailboxes(m.data.data);
    setSequencers((i.data?.data ?? []).filter((x) => x.capabilities.includes("control.warmup")).map((x) => ({ id: x.id, name: x.name })));
  }, [ws]);

  useEffect(() => {
    load();
  }, [load]);

  const header = (
    <PageHeader
      title="Warm-up"
      description="New mailboxes that send cold email straight away land in spam. Warm up each one for about two weeks first, in a sequencer with its own warm-up: EmailBison, Instantly or Smartlead. Outflare turns it on there and shows the score."
      action={<Button variant="ghost" onClick={() => go("mailboxes")}>All mailboxes</Button>}
    />
  );
  if (error) return <div className="space-y-6">{header}<ErrorText>{error}</ErrorText></div>;
  if (!mailboxes) return <div className="space-y-6">{header}<Loading /></div>;

  const inSequencer = (m: Mailbox) => sequencers.some((p) => (m.platforms ?? []).some((x) => x.integration_id === p.id));
  const warming = mailboxes.filter((m) => m.warmup.enabled);
  const notWarming = mailboxes.filter((m) => !m.warmup.enabled && inSequencer(m));
  const outside = mailboxes.filter((m) => !inSequencer(m)).length;
  const scores = warming.flatMap((m) => (m.warmup.platform?.score == null ? [] : [m.warmup.platform.score]));
  const score = scores.length > 0 ? scores.reduce((a, b) => a + b, 0) / scores.length : null;
  const turningOn = warming.filter((m) => m.warmup.waiting_on).length;

  async function change(list: Mailbox[], enabled: boolean) {
    if (await changeWarmup(ws, list, enabled)) load();
  }

  if (sequencers.length === 0) {
    return (
      <div className="space-y-6">
        {header}
        <Empty title="Connect a sequencer to warm up" action={canManage ? <Button onClick={() => go("connectors", "sequencer")}>Connect a sequencer</Button> : undefined}>
          Outflare doesn't send warm-up mail itself. Connect EmailBison, Instantly or Smartlead and add your mailboxes there, and Outflare turns their warm-up on and shows each mailbox's score.
        </Empty>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      {header}
      <Stats cols={4}>
        <Stat label="Warming" value={warming.length} sub={`of ${mailboxes.length} mailboxes`} />
        <Stat label="Warm-up score" value={score === null ? "–" : Math.round(score)} tone={placementTone(score === null ? null : score / 100)} sub={score === null ? "shows after the sequencer's first sync" : "average, from your sequencers"} />
        <Stat label="Turning on" value={turningOn} sub="until the sequencer shows it on" />
        <Stat label="In no sequencer" value={outside} tone={outside > 0 ? "amber" : undefined} sub={outside > 0 ? "add them to one to warm them up" : "all can warm up"} />
      </Stats>

      {canManage && warming.length === 0 && notWarming.length > 0 && (
        <Card
          title="Warm up your mailboxes before sending"
          description="Turn it on for every mailbox now, and keep it on afterwards to protect their reputation."
          action={<Button onClick={() => change(notWarming, true)}>{notWarming.length === 1 ? "Warm up 1 mailbox" : `Warm up ${notWarming.length} mailboxes`}</Button>}
        >
          <p className="text-sm text-muted-foreground">Each mailbox warms up in the sequencer it's in. It runs alongside your sequences and doesn't count towards daily limits.</p>
        </Card>
      )}

      <DataTable
        data={mailboxes}
        rowId={(m) => m.id}
        searchPlaceholder="Search mailboxes"
        empty="No mailboxes match."
        initialSort={[{ id: "warmup", desc: true }]}
        toolbar={canManage && warming.length > 0 && notWarming.length > 0 && <Button variant="ghost" onClick={() => change(notWarming, true)}>Warm up the other {notWarming.length}</Button>}
        mobile={(m) => (
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0">
              <div className="truncate font-medium">{m.email}</div>
              <WarmupCell m={m} />
            </div>
            <Actions m={m} canManage={canManage} canWarm={inSequencer(m)} change={change} />
          </div>
        )}
        columns={[
          { id: "email", header: "Mailbox", accessorFn: (m) => m.email, enableHiding: false, cell: ({ row: { original: m } }) => <span className="font-medium">{m.email}</span> },
          { id: "warmup", header: "Warm-up", accessorFn: (m) => (m.warmup.enabled ? (m.warmup.platform?.score ?? 0) : -1), cell: ({ row: { original: m } }) => <WarmupCell m={m} /> },
          {
            id: "sequencer",
            header: "Sequencer",
            accessorFn: (m) => m.warmup.platform?.name ?? "",
            cell: ({ row: { original: m } }) => <span className="text-muted-foreground">{m.warmup.platform?.name ?? (inSequencer(m) ? "–" : "In none")}</span>,
          },
          { id: "actions", header: "", enableHiding: false, enableSorting: false, cell: ({ row: { original: m } }) => <Actions m={m} canManage={canManage} canWarm={inSequencer(m)} change={change} /> },
        ]}
      />
    </div>
  );
}

function Actions({ m, canManage, canWarm, change }: { m: Mailbox; canManage: boolean; canWarm: boolean; change: (list: Mailbox[], enabled: boolean) => void }) {
  if (!canManage || !(m.warmup.enabled || canWarm)) return null;
  return (
    <div className="flex justify-end gap-1.5">
      <Button variant="ghost" onClick={() => change([m], !m.warmup.enabled)}>{m.warmup.enabled ? "Turn off" : "Turn on"}</Button>
    </div>
  );
}
