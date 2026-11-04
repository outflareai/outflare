import { useState, type FormEvent } from "react";
import { ArrowUpRight, List, Plus } from "lucide-react";
import { errorMessage } from "@/api";
import { Button, Empty, ErrorText, field, toast } from "@/ui";
import { type CatalogInfo, useCatalogWrite } from "@/lib/records";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

export function CRMCollections({
  ws,
  cat,
  canConfigure,
  onOpen,
  onReload,
}: {
  ws: string;
  cat: CatalogInfo;
  canConfigure: boolean;
  onOpen: (type: string) => void;
  onReload: () => void;
}) {
  const [creating, setCreating] = useState(false);
  const lists =
    cat.catalog?.types.filter(
      (t) => t.key.startsWith("list_") && !t.archived,
    ) ?? [];
  return (
    <div className="space-y-5">
      <header className="flex items-center justify-between gap-3">
        <div>
          <h2 className="font-semibold">Lists and pipelines</h2>
          <p className="mt-1 text-sm text-muted-foreground">
            Each entry links to a record and has its own attributes, owner and
            stage.
          </p>
        </div>
        {canConfigure && (
          <Button onClick={() => setCreating(true)}>
            <Plus className="size-3.5" />
            New list
          </Button>
        )}
      </header>
      {!lists.length ? (
        <Empty title="Give your team a shared working list">
          Create a list for a sales pipeline, onboarding process or account
          review.
        </Empty>
      ) : (
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
          {lists.map((list) => (
            <button
              key={list.key}
              className="rounded-xl border bg-card p-4 text-left shadow-xs transition-colors hover:border-primary/40 hover:bg-muted/20"
              onClick={() => onOpen(list.key)}
            >
              <span className="flex items-center gap-3">
                <span className="rounded-lg border bg-muted/30 p-2">
                  <List className="size-4" />
                </span>
                <span className="flex-1 text-sm font-semibold">
                  {list.plural.replace(/ entries$/, " ").trim()}
                </span>
                <ArrowUpRight className="size-3.5 text-muted-foreground" />
              </span>
              <p className="mt-3 text-xs text-muted-foreground">
                {list.fields.filter((f) => !f.archived).length} attributes ·
                Table and board views
              </p>
            </button>
          ))}
        </div>
      )}
      {creating && (
        <CreateList
          ws={ws}
          cat={cat}
          onClose={() => setCreating(false)}
          onSaved={(key) => {
            setCreating(false);
            onReload();
            onOpen(key);
          }}
        />
      )}
    </div>
  );
}
function CreateList({
  ws,
  cat,
  onClose,
  onSaved,
}: {
  ws: string;
  cat: CatalogInfo;
  onClose: () => void;
  onSaved: (type: string) => void;
}) {
  const write = useCatalogWrite(ws);
  const [name, setName] = useState("");
  const [parent, setParent] = useState("person");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  // Persist completed steps in the dialog so retrying a partial setup never creates duplicates.
  const [progress, setProgress] = useState<{
    key?: string;
    options?: string;
    fields: string[];
  }>({ fields: [] });
  async function save(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    setBusy(true);
    setError("");
    const state = { ...progress, fields: [...progress.fields] };
    try {
      if (!state.key) {
        const key = `list_${name
          .trim()
          .toLowerCase()
          .replace(/[^a-z0-9]+/g, "_")}`
          .replace(/_+$/g, "")
          .slice(0, 40);
        if (key === "list" || key === "list_") {
          setError("Use a list name with letters or numbers.");
          return;
        }
        if (cat.catalog?.types.some((t) => t.key === key)) {
          setError("A list with this name already exists.");
          return;
        }
        const { data, error } = await write.createType({
          key,
          name: `${name.trim()} entry`,
          plural: `${name.trim()} entries`,
          description:
            "CRM list entries, each linked to its original record with independent attributes.",
        });
        if (error) {
          setError(errorMessage(error));
          return;
        }
        if (!data || !("key" in data)) return;
        state.key = data.key;
        setProgress({ ...state });
      }
      if (!state.options) {
        const { data, error } = await write.createOptionSet({
          key: `${state.key}_stages`,
          name: `${name.trim()} stages`,
          options: [
            { key: "new", label: "New" },
            { key: "in_progress", label: "In progress" },
            { key: "done", label: "Done" },
          ],
        });
        if (error) {
          setError(errorMessage(error));
          return;
        }
        if (!data || !("key" in data)) return;
        state.options = data.key;
        setProgress({ ...state });
      }
      const fields = [
        {
          key: "outflare_parent_record",
          label: "Linked record",
          value_type: "record_reference" as const,
          target_type: parent,
        },
        {
          key: "stage",
          label: "Stage",
          value_type: "status" as const,
          option_set: state.options,
        },
        {
          key: "owner",
          label: "Owner",
          value_type: "actor_reference" as const,
        },
      ];
      for (const f of fields) {
        if (state.fields.includes(f.key)) continue;
        const { data, error } = await write.createField(state.key, {
          ...f,
          filterable: true,
        });
        if (error) {
          setError(errorMessage(error));
          return;
        }
        if (!data || !("key" in data)) return;
        state.fields.push(f.key);
        setProgress({ ...state });
      }
      toast(
        "List created. Add entries and customize its attributes in CRM settings.",
      );
      onSaved(state.key);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Dialog open onOpenChange={(v) => !v && !busy && onClose()}>
      <DialogContent>
        <form className="space-y-4" onSubmit={save}>
          <DialogHeader>
            <DialogTitle>New list</DialogTitle>
            <DialogDescription>
              Track a process without duplicating the underlying contact,
              company or deal. Entries get their own stage and owner.
            </DialogDescription>
          </DialogHeader>
          <label className="block text-sm">
            List name
            <input
              required
              autoFocus
              maxLength={60}
              disabled={!!progress.key}
              className={`${field} mt-1 w-full`}
              placeholder="Enterprise prospects"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </label>
          <label className="block text-sm">
            What goes in this list?
            <select
              aria-label="List record type"
              className={`${field} mt-1 w-full`}
              disabled={!!progress.key}
              value={parent}
              onChange={(e) => setParent(e.target.value)}
            >
              {cat.catalog?.types
                .filter((t) => !t.archived && !t.key.startsWith("list_"))
                .map((t) => (
                  <option key={t.key} value={t.key}>
                    {t.plural}
                  </option>
                ))}
            </select>
          </label>
          {progress.key && error && (
            <p className="rounded-lg border bg-muted/30 p-3 text-xs">
              The list object was created. Retry to finish its remaining
              attributes, or find it in CRM settings.
            </p>
          )}
          <ErrorText>{error}</ErrorText>
          <DialogFooter>
            <Button
              type="button"
              variant="ghost"
              disabled={busy}
              onClick={onClose}
            >
              Cancel
            </Button>
            <Button disabled={busy || !name.trim()}>
              {busy
                ? "Creating…"
                : progress.key
                  ? "Finish setup"
                  : "Create list"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
