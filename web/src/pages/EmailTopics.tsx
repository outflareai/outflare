// Email topics: what people can choose to get, such as product updates.
// Lifecycle templates belong to one, and the preference page behind every
// unsubscribe link lists them. Who chose what is a consent on the person's
// customer record.

import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { EmailTopic } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Empty, ErrorText, field, Input, Loading, Menu, PageHeader, toast } from "../ui";
import { plural } from "../format";
import { DataTable, type ColumnDef } from "@/components/data-table";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import type { Go } from "./Lifecycle";
import { GuardrailsCard } from "./EmailGuardrails";

const columns: ColumnDef<EmailTopic>[] = [
  {
    id: "name", header: "Topic", accessorFn: (t) => t.name,
    cell: ({ row: { original: t } }) => (
      <span className="block min-w-0">
        <span className="block truncate font-medium">{t.name}</span>
        {t.description && <span className="block truncate text-[12px] text-muted-foreground">{t.description}</span>}
      </span>
    ),
  },
  { id: "key", header: "Key", accessorFn: (t) => t.key, cell: ({ row: { original: t } }) => <span className="font-mono text-[12px]">{t.key}</span> },
  {
    id: "default", header: "Who gets it", accessorFn: (t) => (t.subscribed_by_default ? 0 : 1),
    cell: ({ row: { original: t } }) => (t.subscribed_by_default ? "Everyone, until they leave" : "Only people who opt in"),
  },
  { id: "visibility", header: "Preference page", accessorFn: (t) => t.visibility, cell: ({ row: { original: t } }) => (t.visibility === "public" ? "Listed" : <Badge tone="grey">Only to subscribers</Badge>) },
  { id: "templates", header: "Templates", accessorFn: (t) => t.templates },
  { id: "sent", header: "Sent, 30 days", accessorFn: (t) => t.sent_30d },
];

export function Topics({ ws, canManage, go }: { ws: string; canManage: boolean; go: Go }) {
  const [topics, setTopics] = useState<EmailTopic[] | null>(null);
  const [status, setStatus] = useState<"active" | "archived">("active");
  const [editing, setEditing] = useState<EmailTopic | "new" | null>(null);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/email-topics", { params: { path: { ws }, query: { status } } });
    if (error) setError(errorMessage(error));
    setTopics(data?.data ?? []);
  }, [ws, status]);
  useEffect(() => {
    load();
  }, [load]);

  async function setArchived(t: EmailTopic, archived: boolean) {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/email-topics/{id}", { params: { path: { ws, id: t.id } }, body: { status: archived ? "archived" : "active" } });
    if (error) return toast(errorMessage(error), "error");
    toast(archived ? `Archived ${t.name}. Templates in it keep it until you change them.` : `Restored ${t.name}.`);
    load();
  }

  if (!topics) return <Loading />;
  return (
    <div className="space-y-6">
      <PageHeader
        title="Topics"
        description="What people can choose to get, such as product updates or a newsletter. Put lifecycle templates in a topic, and the unsubscribe link lets people leave just that one. Their choices are saved on their customer record."
        action={canManage && <Button onClick={() => setEditing("new")}>New topic</Button>}
      />
      <ErrorText>{error}</ErrorText>
      {topics.length === 0 && status === "active" ? (
        <Empty title="No topics yet" action={canManage && <Button onClick={() => setEditing("new")}>New topic</Button>}>
          Without topics, unsubscribing from one lifecycle email stops them all. Add a topic for each kind of email you send, such as product updates, tips or a newsletter. Receipts and other transactional email never need one.
        </Empty>
      ) : (
        <DataTable
          columns={canManage ? [...columns, {
            id: "actions", header: "", enableSorting: false,
            cell: ({ row: { original: t } }) => (
              <Menu aria={`Actions for ${t.name}`} items={[
                { label: "Edit", onClick: () => setEditing(t) },
                { label: "See its emails", onClick: () => go("emails", `topic:${t.key}`) },
                t.status === "active" ? { label: "Archive", onClick: () => setArchived(t, true) } : { label: "Restore", onClick: () => setArchived(t, false) },
              ]} />
            ),
          }] : columns}
          data={topics}
          rowId={(t) => t.id}
          onRowClick={canManage ? (t) => setEditing(t) : undefined}
          searchPlaceholder="Search topics"
          toolbar={
            <select aria-label="Show" className={field} value={status} onChange={(e) => setStatus(e.target.value as "active" | "archived")}>
              <option value="active">Active</option>
              <option value="archived">Archived</option>
            </select>
          }
          empty={status === "archived" ? "No archived topics." : "No topics match."}
          mobile={(t) => (
            <div className="flex min-w-0 items-center gap-3">
              <span className="min-w-0 flex-1">
                <span className="block truncate text-[13px] font-medium">{t.name}</span>
                <span className="block truncate text-[12px] text-muted-foreground">{plural(t.templates, "template")} · {t.subscribed_by_default ? "everyone" : "opt-in"}</span>
              </span>
            </div>
          )}
        />
      )}
      <GuardrailsCard ws={ws} canManage={canManage} />
      {editing && <TopicDialog ws={ws} topic={editing === "new" ? null : editing} onClose={() => setEditing(null)} onSaved={() => { setEditing(null); load(); }} />}
    </div>
  );
}

function TopicDialog({ ws, topic, onClose, onSaved }: { ws: string; topic: EmailTopic | null; onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState(topic?.name ?? "");
  const [description, setDescription] = useState(topic?.description ?? "");
  const [byDefault, setByDefault] = useState(topic?.subscribed_by_default ?? true);
  const [visibility, setVisibility] = useState<"public" | "private">(topic?.visibility ?? "public");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const body = { name, description, subscribed_by_default: byDefault, visibility };
    const res = topic
      ? await api.PATCH("/v1/workspaces/{ws}/email-topics/{id}", { params: { path: { ws, id: topic.id } }, body })
      : await api.POST("/v1/workspaces/{ws}/email-topics", { params: { path: { ws } }, body });
    setBusy(false);
    if (res.error || !res.data) return setError(errorMessage(res.error));
    toast(topic ? `Saved ${res.data.name}.` : `Added ${res.data.name}. Pick it on a lifecycle template to use it.`);
    onSaved();
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{topic ? `Edit ${topic.name}` : "New topic"}</DialogTitle>
          <DialogDescription>People see the name and description on the preference page behind the unsubscribe link.</DialogDescription>
        </DialogHeader>
        <form onSubmit={save} className="space-y-3">
          <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Product updates" maxLength={100} required autoFocus />
          <Input label="Description" value={description} onChange={(e) => setDescription(e.target.value)} placeholder="New features, once a month." maxLength={500} />
          {topic && <p className="text-[12px] text-muted-foreground">Key <span className="font-mono">{topic.key}</span>: what your app and the API send by. It can't change.</p>}
          <fieldset className="space-y-2 text-[13px]">
            <legend className="mb-1 font-medium">Who gets it</legend>
            <label className="flex items-start gap-2">
              <input type="radio" name="default" className="mt-0.5" checked={byDefault} onChange={() => setByDefault(true)} />
              <span>Everyone, until they leave it<span className="block text-[12px] text-muted-foreground">For email people expect from you, such as product updates.</span></span>
            </label>
            <label className="flex items-start gap-2">
              <input type="radio" name="default" className="mt-0.5" checked={!byDefault} onChange={() => setByDefault(false)} />
              <span>Only people who opt in<span className="block text-[12px] text-muted-foreground">For extras such as a beta program. People join on the preference page, or your app records their consent through the API.</span></span>
            </label>
          </fieldset>
          <label className="flex items-start gap-2 text-[13px]">
            <input type="checkbox" className="mt-0.5" checked={visibility === "public"} onChange={(e) => setVisibility(e.target.checked ? "public" : "private")} />
            <span>List it on the preference page<span className="block text-[12px] text-muted-foreground">Off: only people who get it see it there.</span></span>
          </label>
          <ErrorText>{error}</ErrorText>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            <Button disabled={busy || !name.trim()}>{topic ? "Save" : "Add topic"}</Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
