// A brand's own playbooks (docs/playbooks.md, "Your own playbooks"): a
// title and a purpose around rules, journeys, lists, signal types, scores,
// routing rules, emails and metrics the brand already has. The builder is one full page: name it, tick what
// belongs to it, save. Its page then shows what each part has done.

import { useEffect, useState, type FormEvent } from "react";
import type { Schemas } from "@outflare/sdk";
import { PlaybookHomeSection, PlaybookLiftSection, partKind } from "@/components/playbook-home";
import { track } from "@/lib/analytics";
import { api, errorMessage } from "../api";
import { plural } from "../format";
import { Badge, Button, Card, Empty, ErrorText, Input, Loading, PageHeader, toast } from "../ui";
import type { Tab } from "../App";

export type OwnPlaybook = Schemas["OwnPlaybook"];
type Kind = OwnPlaybook["parts"][number]["kind"];
type Choice = { kind: Kind; id: string; name: string; note: string };

const kindOrder: Kind[] = ["rule", "journey", "smart_list", "signal_type", "score", "routing_rule", "email", "metric"];
const kindTitle: Record<Kind, string> = { rule: "Rules", journey: "Journeys", smart_list: "Lists", signal_type: "Signal types", score: "Scores", routing_rule: "Routing rules", email: "Emails", metric: "Metrics" };
const kindHelp: Record<Kind, string> = {
  rule: "What it watches for.",
  journey: "What happens next.",
  smart_list: "Who it is about.",
  signal_type: "What it puts in Needs attention.",
  score: "How it ranks them.",
  routing_rule: "Who gets them.",
  email: "What it sends.",
  metric: "What it measures.",
};
const kindTab: Record<Kind, Tab> = { rule: "rules", journey: "journeys", smart_list: "lists", signal_type: "signals", score: "people", routing_rule: "people", email: "templates", metric: "people" };
const kindItem: Partial<Record<Kind, string>> = { score: "scores", routing_rule: "routing", metric: "metrics" };
const statusText = (s: string) => (s === "active" ? "On" : s === "paused" ? "Paused" : s === "draft" ? "Draft" : s);
const partId = (p: { kind: Kind; key: string }) => `${p.kind} ${p.key}`;

// Everything the brand has that a playbook can group.
function useChoices(brandId: string): { choices: Choice[] | null; error: string } {
  const [choices, setChoices] = useState<Choice[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    let live = true;
    const path = { params: { path: { brand: brandId } } };
    void Promise.all([
      api.GET("/v1/brands/{brand}/rules", path),
      api.GET("/v1/brands/{brand}/journeys", path),
      api.GET("/v1/brands/{brand}/smart-lists", path),
      api.GET("/v1/brands/{brand}/signal-types", path),
      api.GET("/v1/brands/{brand}/scores", path),
      api.GET("/v1/brands/{brand}/routing", path),
      api.GET("/v1/brands/{brand}/email-templates", path),
      api.GET("/v1/brands/{brand}/metrics", path),
    ]).then(([rules, journeys, lists, types, scores, routing, emails, metrics]) => {
      if (!live) return;
      const failed = rules.error ?? journeys.error ?? lists.error ?? types.error;
      if (failed) return setError(errorMessage(failed));
      setChoices([
        ...(rules.data?.data ?? []).filter((x) => x.status !== "archived").map((x) => ({ kind: "rule" as const, id: x.id, name: x.name, note: statusText(x.status) })),
        ...(journeys.data?.data ?? []).filter((x) => x.status !== "archived").map((x) => ({ kind: "journey" as const, id: x.id, name: x.name, note: statusText(x.status) })),
        ...(lists.data?.data ?? []).map((x) => ({ kind: "smart_list" as const, id: x.id, name: x.name, note: x.kind === "static" ? "Fixed list" : "Smart list" })),
        ...(types.data?.data ?? []).map((x) => ({ kind: "signal_type" as const, id: x.key, name: x.label, note: "" })),
        // These four are extras: someone who can't read one of them still builds a playbook from the rest.
        ...(scores.data?.data ?? []).filter((x) => x.status !== "archived").map((x) => ({ kind: "score" as const, id: x.id, name: x.label, note: x.record_type === "account" ? "Companies" : x.record_type === "deal" ? "Deals" : "People" })),
        ...(routing.data?.data ?? []).map((x) => ({ kind: "routing_rule" as const, id: x.id, name: x.name, note: x.status === "paused" ? "Paused" : "On" })),
        ...(emails.data?.data ?? []).filter((x) => x.status !== "archived").map((x) => ({ kind: "email" as const, id: x.id, name: x.name, note: "" })),
        ...(metrics.data?.data ?? []).filter((x) => x.status !== "archived").map((x) => ({ kind: "metric" as const, id: x.id, name: x.name, note: "" })),
      ]);
    });
    return () => {
      live = false;
    };
  }, [brandId]);
  return { choices, error };
}

export function OwnPlaybookBuilder({ brandId, playbook, onDone }: { brandId: string; playbook?: OwnPlaybook; onDone: (id?: string) => void }) {
  const { choices, error: loadError } = useChoices(brandId);
  const [title, setTitle] = useState(playbook?.title ?? "");
  const [summary, setSummary] = useState(playbook?.summary ?? "");
  const [picked, setPicked] = useState<string[]>(() => (playbook?.parts ?? []).map(partId));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function save(e: FormEvent) {
    e.preventDefault();
    if (busy || !choices) return;
    if (!title.trim()) return setError("Give the playbook a name.");
    setError("");
    setBusy(true);
    const parts = choices.filter((c) => picked.includes(`${c.kind} ${c.id}`)).map((c) => ({ kind: c.kind, id: c.id }));
    const body = { title: title.trim(), summary: summary.trim(), parts };
    try {
      const res = playbook
        ? await api.PATCH("/v1/brands/{brand}/own-playbooks/{id}", { params: { path: { brand: brandId, id: playbook.id } }, body })
        : await api.POST("/v1/brands/{brand}/own-playbooks", { params: { path: { brand: brandId } }, body });
      if (res.error || !res.data) return setError(errorMessage(res.error));
      if (!("id" in res.data)) {
        toast("Sent for approval.");
        return onDone(playbook?.id);
      }
      track("ui.own_playbook_saved", { parts: parts.length, is_new: !playbook });
      toast(playbook ? "Playbook saved." : "Playbook made.");
      onDone(res.data.id);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className="space-y-6" onSubmit={save}>
      <PageHeader
        title={playbook ? "Change playbook" : "New playbook"}
        back={{ label: playbook ? playbook.title : "Playbooks", onClick: () => onDone(playbook?.id) }}
        description="A playbook of your own is a name around the rules, journeys, lists, signals, scores, routing rules, emails and metrics you already have, so what works together is found, switched on and measured together. Saving changes none of them."
        action={
          <>
            <Button variant="ghost" type="button" onClick={() => onDone(playbook?.id)}>Cancel</Button>
            <Button variant="primary" type="submit" disabled={busy || !choices}>{playbook ? "Save playbook" : "Make playbook"}</Button>
          </>
        }
      />
      <ErrorText>{error || loadError}</ErrorText>
      <Card title="What it is">
        <div className="grid gap-4 md:grid-cols-2">
          <Input label="Name" value={title} maxLength={80} autoFocus placeholder="Save accounts that go quiet" onChange={(e) => setTitle(e.target.value)} />
          <Input label="What it is for" value={summary} maxLength={300} placeholder="Spot companies that stop using the product and get their owner to call." onChange={(e) => setSummary(e.target.value)} />
        </div>
      </Card>

      {!choices && !loadError && <Loading />}
      {choices && choices.length === 0 && (
        <Empty title="Nothing to group yet">Make a rule, a journey or a list first, or set up a ready-made playbook, then come back to give them a name.</Empty>
      )}
      {choices && choices.length > 0 && (
        <div className="grid items-start gap-6 lg:grid-cols-2">
          {kindOrder.map((kind) => {
            const list = choices.filter((c) => c.kind === kind);
            if (list.length === 0) return null;
            const on = list.filter((c) => picked.includes(`${c.kind} ${c.id}`)).length;
            return (
              <Card key={kind} title={kindTitle[kind]} description={kindHelp[kind]} action={<span className="text-xs text-muted-foreground">{on} picked</span>}>
                <ul className="max-h-80 divide-y overflow-y-auto">
                  {list.map((c) => {
                    const key = `${c.kind} ${c.id}`;
                    return (
                      <li key={key}>
                        <label className="flex cursor-pointer items-center gap-3 py-2 text-sm">
                          <input
                            type="checkbox"
                            className="size-4 shrink-0"
                            checked={picked.includes(key)}
                            onChange={(e) => setPicked((now) => (e.target.checked ? [...now, key] : now.filter((k) => k !== key)))}
                          />
                          <span className="min-w-0 flex-1 truncate">{c.name}</span>
                          {c.note && <span className="shrink-0 text-xs text-muted-foreground">{c.note}</span>}
                        </label>
                      </li>
                    );
                  })}
                </ul>
              </Card>
            );
          })}
        </div>
      )}
    </form>
  );
}

export function OwnPlaybookPage({
  brandId,
  playbook: p,
  canManage,
  go,
  onClose,
  onChanged,
}: {
  brandId: string;
  playbook: OwnPlaybook;
  canManage: boolean;
  go: (tab: Tab, item?: string) => void;
  onClose: () => void;
  onChanged: () => void;
}) {
  const [editing, setEditing] = useState(false);
  const [removing, setRemoving] = useState(false);
  const [busy, setBusy] = useState(false);

  if (editing)
    return (
      <OwnPlaybookBuilder
        brandId={brandId}
        playbook={p}
        onDone={() => {
          setEditing(false);
          onChanged();
        }}
      />
    );

  async function remove() {
    if (busy) return;
    setBusy(true);
    try {
      const { error } = await api.DELETE("/v1/brands/{brand}/own-playbooks/{id}", { params: { path: { brand: brandId, id: p.id } } });
      if (error) return toast(errorMessage(error), "error");
      track("ui.own_playbook_removed", { parts: p.parts.length });
      toast("Playbook removed. Its rules, journeys and lists are still there.");
      onClose();
    } catch (e) {
      toast(errorMessage(e), "error");
    } finally {
      setBusy(false);
    }
  }

  const counts = kindOrder.map((k) => [k, p.parts.filter((x) => x.kind === k).length] as const).filter(([, n]) => n > 0);
  return (
    <div className="space-y-6">
      <PageHeader
        title={<>{p.title} <Badge tone="blue">Yours</Badge></>}
        back={{ label: "Playbooks", onClick: onClose }}
        description={p.summary || "A playbook of your own."}
        action={
          canManage && (
            <>
              <Button variant="ghost" onClick={() => setRemoving(true)}>Remove</Button>
              <Button variant="primary" onClick={() => setEditing(true)}>Change</Button>
            </>
          )
        }
      />
      {removing && (
        <div className="flex flex-wrap items-center gap-3 rounded-lg border border-red-500/40 bg-red-500/5 p-3 text-sm" role="alert">
          <span className="min-w-0 flex-1">Remove this playbook? Its {plural(p.parts.length, "part")} stay exactly as they are; only the grouping goes.</span>
          <Button variant="ghost" onClick={() => setRemoving(false)}>Keep it</Button>
          <Button variant="danger" disabled={busy} onClick={() => void remove()}>Remove playbook</Button>
        </div>
      )}
      {p.parts.length === 0 ? (
        <Empty title="Nothing in this playbook yet">
          {canManage ? <button className="underline underline-offset-4" onClick={() => setEditing(true)}>Pick the rules, journeys and lists that belong to it.</button> : "Nobody has added anything to it yet."}
        </Empty>
      ) : (
        <div className="max-w-3xl space-y-4 text-[13px]">
          <p className="text-muted-foreground">{counts.map(([k, n]) => `${n} ${n === 1 ? partKind[k].toLowerCase() : kindTitle[k].toLowerCase()}`).join(", ")}. Open a part to change it or turn it on.</p>
          <PlaybookHomeSection home={p} go={go} title="What it has done" />
          <PlaybookLiftSection brand={brandId} playbook={p.id} go={go} own />
          <p className="text-[12px] text-muted-foreground">
            Want to add to it?{" "}
            {kindOrder.slice(0, 3).map((k, i) => (
              <span key={k}>
                {i > 0 && ", "}
                <button className="underline underline-offset-4" onClick={() => go(kindTab[k], kindItem[k])}>{kindTitle[k]}</button>
              </span>
            ))}{" "}
            are where you make new parts; then come back and press Change.
          </p>
        </div>
      )}
    </div>
  );
}
