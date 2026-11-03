import { useEffect, useState } from "react";
import type { Member, SupportHandoffTopic } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, ErrorText, Loading, confirmAction, field, toast } from "../ui";
import { Icon } from "../icons";
import { cn } from "@/lib/utils";
import { useBrand } from "@/lib/brand";

// Hand-off topics, in Support settings: what conversations are about, for
// the AI assistant to tag them, and who takes each one it hands over.

type Team = { id: string; name: string };
type Draft = { name: string; description: string; assignee_id: string; team_id: string; always_handoff: boolean; brand: string };

const empty: Draft = { name: "", description: "", assignee_id: "", team_id: "", always_handoff: false, brand: "" };

export function SupportHandoffTopics({ ws, canManage }: { ws: string; canManage: boolean }) {
  const { brands } = useBrand(ws);
  const [topics, setTopics] = useState<SupportHandoffTopic[] | undefined>(undefined);
  const [members, setMembers] = useState<Member[]>([]);
  const [teams, setTeams] = useState<Team[]>([]);
  // The topic being edited: "new", an id, or none.
  const [editing, setEditing] = useState<string | null>(null);
  const [draft, setDraft] = useState<Draft>(empty);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/support/handoff-topics", { params: { path: { ws } } }).then(({ data, error }) => {
      if (error) return setError(errorMessage(error));
      setTopics(data.data);
    });
    api.GET("/v1/workspaces/{ws}/members", { params: { path: { ws } } }).then(({ data }) => setMembers((data as { data?: Member[] } | undefined)?.data ?? []));
    api.GET("/v1/workspaces/{ws}/teams", { params: { path: { ws } } }).then(({ data }) => setTeams(((data as { data?: Team[] } | undefined)?.data ?? []) as Team[]));
  }, [ws]);

  function edit(t: SupportHandoffTopic | null) {
    setError("");
    if (!t) {
      setDraft(empty);
      return setEditing("new");
    }
    setDraft({ name: t.name, description: t.description, assignee_id: t.assignee?.id ?? "", team_id: t.team?.id ?? "", always_handoff: t.always_handoff, brand: t.brand_id ?? "" });
    setEditing(t.id);
  }

  async function save() {
    setBusy(true);
    setError("");
    const body = {
      name: draft.name.trim(), description: draft.description.trim(), always_handoff: draft.always_handoff,
      assignee_id: draft.assignee_id || null, team_id: draft.team_id || null,
    };
    const res = editing === "new"
      ? await api.POST("/v1/workspaces/{ws}/support/handoff-topics", { params: { path: { ws } }, body: { ...body, brand: draft.brand || null } })
      : await api.PATCH("/v1/workspaces/{ws}/support/handoff-topics/{id}", { params: { path: { ws, id: editing! } }, body });
    setBusy(false);
    if (res.error) return setError(errorMessage(res.error));
    const saved = res.data as SupportHandoffTopic;
    setTopics((l) => [...(l ?? []).filter((x) => x.id !== saved.id), saved].sort((a, b) => a.name.localeCompare(b.name)));
    setEditing(null);
    toast(editing === "new" ? `Added ${saved.name}` : `Saved ${saved.name}`);
  }

  async function remove(t: SupportHandoffTopic) {
    if (!(await confirmAction(`Remove ${t.name}?`, "Conversations tagged with it lose the tag. Who has them doesn't change.", "Remove"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/support/handoff-topics/{id}", { params: { path: { ws, id: t.id } } });
    if (error) return toast(errorMessage(error), "error");
    setTopics((l) => (l ?? []).filter((x) => x.id !== t.id));
    setEditing(null);
  }

  if (topics === undefined && !error) return <Loading />;
  const who = (t: SupportHandoffTopic) => [t.assignee?.name, t.team?.name].filter(Boolean).join(" · ") || "Anyone";
  const brandName = (id: string | null) => (id ? brands.find((b) => b.id === id)?.name ?? "A brand" : null);

  const form = (
    <div className="space-y-2 rounded-md border bg-muted/30 p-3">
      <div className="flex flex-col gap-2 sm:flex-row">
        <input aria-label="Topic name" className={cn(field, "sm:w-48")} value={draft.name} maxLength={60} placeholder="Billing"
          onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
        <input aria-label="What belongs in it" className={cn(field, "flex-1")} value={draft.description} maxLength={300}
          placeholder="Invoices, payments, plans and refunds" onChange={(e) => setDraft({ ...draft, description: e.target.value })} />
      </div>
      <div className="flex flex-col gap-2 sm:flex-row">
        <select aria-label="Person who takes it" className={cn(field, "flex-1")} value={draft.assignee_id} onChange={(e) => setDraft({ ...draft, assignee_id: e.target.value })}>
          <option value="">No one in particular</option>
          {members.map((m) => <option key={m.user_id} value={m.user_id}>{m.name || m.email}</option>)}
        </select>
        <select aria-label="Team that takes it" className={cn(field, "flex-1")} value={draft.team_id} onChange={(e) => setDraft({ ...draft, team_id: e.target.value })}>
          <option value="">No team</option>
          {teams.map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}
        </select>
        {editing === "new" && brands.length > 0 && (
          <select aria-label="Brand" className={cn(field, "flex-1")} value={draft.brand} onChange={(e) => setDraft({ ...draft, brand: e.target.value })}>
            <option value="">Every brand</option>
            {brands.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
          </select>
        )}
      </div>
      <label className="flex items-center gap-2 text-[13px]">
        <input type="checkbox" checked={draft.always_handoff} onChange={(e) => setDraft({ ...draft, always_handoff: e.target.checked })} />
        Always hand it to your team: the assistant never answers this topic
      </label>
      {error && <ErrorText>{error}</ErrorText>}
      <div className="flex justify-end gap-2">
        <Button variant="ghost" onClick={() => setEditing(null)}>Cancel</Button>
        <Button disabled={busy || !draft.name.trim()} onClick={save}>{busy ? "Saving…" : "Save"}</Button>
      </div>
    </div>
  );

  return (
    <div className="space-y-3 rounded-lg border p-4">
      <div className="flex items-start gap-3">
        <div className="min-w-0 flex-1">
          <p className="flex items-center gap-1.5 text-[13px] font-medium">
            <Icon name="ai" className="size-3.5 text-violet-600 dark:text-violet-400" />Hand-off topics
          </p>
          <p className="mt-0.5 text-xs text-muted-foreground">
            What your conversations are about. The AI assistant tags each conversation with one, and when it leaves one to your team, gives it to the topic's person and team.
          </p>
        </div>
        {canManage && editing === null && <Button variant="ghost" onClick={() => edit(null)}>Add topic</Button>}
      </div>
      {editing === "new" && form}
      {topics && topics.length === 0 && editing === null && (
        <p className="text-xs text-muted-foreground">No topics yet. Add Billing, Bugs or Sales, say, to send each hand-off to the right person.</p>
      )}
      {topics && topics.length > 0 && (
        <ul className="divide-y rounded-md border">
          {topics.map((t) =>
            editing === t.id ? (
              <li key={t.id} className="p-2">{form}</li>
            ) : (
              <li key={t.id} className="flex flex-wrap items-center gap-2 px-3 py-2">
                <div className="min-w-0 flex-1">
                  <p className="text-[13px] font-medium">
                    {t.name}
                    {brandName(t.brand_id) && <span className="ml-1.5 text-xs font-normal text-muted-foreground">{brandName(t.brand_id)}</span>}
                    {t.always_handoff && <span className="ml-1.5 rounded bg-muted px-1.5 py-0.5 text-[11px] font-normal text-muted-foreground">Always to your team</span>}
                  </p>
                  {t.description && <p className="truncate text-xs text-muted-foreground">{t.description}</p>}
                </div>
                <span className="text-xs text-muted-foreground">{who(t)}</span>
                {canManage && (
                  <>
                    <button type="button" className="text-xs text-muted-foreground underline" onClick={() => edit(t)}>Edit</button>
                    <button type="button" className="text-xs text-muted-foreground underline" onClick={() => remove(t)}>Remove</button>
                  </>
                )}
              </li>
            ),
          )}
        </ul>
      )}
      {error && editing === null && <ErrorText>{error}</ErrorText>}
    </div>
  );
}
