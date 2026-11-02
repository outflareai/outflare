import { useState, type FormEvent } from "react";
import type { Schemas } from "@outflare/sdk";
import {
  Plus,
  LockKeyhole,
  Link2,
  Type,
  SlidersHorizontal,
} from "lucide-react";
import { api, errorMessage } from "@/api";
import { Button, ErrorText, field, toast } from "@/ui";
import { type CatalogInfo } from "@/lib/records";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

export function CRMSettings({
  ws,
  cat,
  canManage,
  reload,
}: {
  ws: string;
  cat: CatalogInfo;
  canManage: boolean;
  reload: () => void;
}) {
  const [type, setType] = useState("person");
  const [showArchived, setShowArchived] = useState(false);
  const [editingOptions, setEditingOptions] = useState<string | null>(null);
  const [editingField, setEditingField] = useState<Schemas["Field"] | null>(
    null,
  );
  const [adding, setAdding] = useState<"object" | "field" | null>(null);
  const obj = cat.catalog?.types.find((t) => t.key === type);
  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center gap-3">
        <div className="flex-1">
          <h2 className="font-semibold">Objects and attributes</h2>
          <p className="mt-1 text-sm text-muted-foreground">
            Define your team's data once. Every record uses the same fields and
            choices.
          </p>
        </div>
        {canManage && (
          <Button onClick={() => setAdding("object")}>
            <Plus className="size-3.5" />
            New object
          </Button>
        )}
      </div>
      {!canManage && (
        <p className="flex items-center gap-2 rounded-lg border bg-muted/20 p-3 text-sm text-muted-foreground">
          <LockKeyhole className="size-4" />
          You can browse the schema. A workspace administrator can change it.
        </p>
      )}
      <label className="flex items-center gap-2 text-xs text-muted-foreground">
        <input
          type="checkbox"
          checked={showArchived}
          onChange={(e) => setShowArchived(e.target.checked)}
        />
        Show archived attributes
      </label>
      <div className="grid gap-5 md:grid-cols-[210px_1fr]">
        <div className="space-y-1">
          {cat.catalog?.types
            .filter((t) => !t.archived)
            .map((t) => (
              <button
                key={t.key}
                className={`flex w-full items-center gap-2 rounded-lg px-3 py-2.5 text-left text-sm ${type === t.key ? "bg-muted font-medium" : "text-muted-foreground hover:bg-muted/40"}`}
                onClick={() => setType(t.key)}
              >
                <SlidersHorizontal className="size-3.5" />
                {t.plural}
              </button>
            ))}
        </div>
        <section className="rounded-xl border bg-card">
          <div className="flex items-center gap-3 border-b p-4">
            <div className="flex-1">
              <h3 className="text-sm font-semibold">
                {obj?.plural} attributes
              </h3>
              <p className="mt-1 text-xs text-muted-foreground">
                {obj?.description}
              </p>
            </div>
            {canManage && (
              <Button variant="ghost" onClick={() => setAdding("field")}>
                <Plus className="size-3.5" />
                Add attribute
              </Button>
            )}
          </div>
          <div className="divide-y">
            {(obj?.fields ?? [])
              .filter((f) => showArchived || !f.archived)
              .map((f) => (
                <div key={f.key} className="flex items-center gap-3 px-4 py-3">
                  <span className="rounded-lg border p-2 text-muted-foreground">
                    {f.value_type === "record_reference" ? (
                      <Link2 className="size-3.5" />
                    ) : (
                      <Type className="size-3.5" />
                    )}
                  </span>
                  <div className="min-w-0 flex-1">
                    <p className="text-sm font-medium">{f.label}</p>
                    <p className="text-xs text-muted-foreground">
                      {f.description || f.value_type.replaceAll("_", " ")}
                      {f.target_type ? ` → ${f.target_type}` : ""}
                      {f.multiple ? " · Multiple values" : ""}
                    </p>
                  </div>
                  {canManage &&
                    f.option_set &&
                    !cat.catalog?.option_sets.find(
                      (o) => o.key === f.option_set,
                    )?.builtin && (
                      <Button
                        variant="ghost"
                        onClick={() => setEditingOptions(f.option_set!)}
                      >
                        Edit choices
                      </Button>
                    )}
                  {canManage && !f.builtin && (
                    <Button variant="ghost" onClick={() => setEditingField(f)}>
                      Edit attribute
                    </Button>
                  )}
                  <span className="text-xs text-muted-foreground">
                    {f.write_policy === "computed"
                      ? "Calculated"
                      : f.builtin
                        ? "Built in"
                        : "Custom"}
                  </span>
                </div>
              ))}
          </div>
        </section>
      </div>
      {editingOptions &&
        cat.catalog?.option_sets.find((o) => o.key === editingOptions) && (
          <OptionEditor
            ws={ws}
            optionSet={
              cat.catalog.option_sets.find((o) => o.key === editingOptions)!
            }
            onClose={() => setEditingOptions(null)}
            onSaved={() => {
              setEditingOptions(null);
              reload();
            }}
          />
        )}
      {editingField && (
        <AttributeEditor
          ws={ws}
          type={type}
          attribute={editingField}
          onClose={() => setEditingField(null)}
          onSaved={() => {
            setEditingField(null);
            reload();
          }}
        />
      )}
      {adding && (
        <SchemaDialog
          ws={ws}
          mode={adding}
          type={type}
          cat={cat}
          onClose={() => setAdding(null)}
          onSaved={() => {
            setAdding(null);
            reload();
          }}
        />
      )}
    </div>
  );
}

function SchemaDialog({
  ws,
  mode,
  type,
  cat,
  onClose,
  onSaved,
}: {
  ws: string;
  mode: "object" | "field";
  type: string;
  cat: CatalogInfo;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [name, setName] = useState("");
  const [plural, setPlural] = useState("");
  const [valueType, setValueType] =
    useState<Schemas["Field"]["value_type"]>("text");
  const [target, setTarget] = useState("account");
  const [multiple, setMultiple] = useState(false);
  const [options, setOptions] = useState("");
  const [optionSet, setOptionSet] = useState("");
  const [createdSet, setCreatedSet] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const choice = valueType === "select" || valueType === "status";
  const key = (s: string) =>
    s
      .trim()
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "_")
      .replace(/^_+|_+$/g, "");
  async function save(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      if (mode === "object") {
        const { data, error } = await api.POST(
          "/v1/workspaces/{ws}/catalog/types",
          {
            params: { path: { ws } },
            body: {
              name: name.trim(),
              ...(plural.trim() ? { plural: plural.trim() } : {}),
            },
          },
        );
        if (error) {
          setError(errorMessage(error));
          return;
        }
        if (!data || !("key" in data)) return;
      } else {
        let set = optionSet || createdSet;
        if (choice && !set) {
          const labels = options
            .split("\n")
            .map((x) => x.trim())
            .filter(Boolean);
          const choices = labels.map((label) => ({ key: key(label), label }));
          if (
            !choices.length ||
            choices.some((x) => !x.key) ||
            new Set(choices.map((x) => x.key)).size !== choices.length
          ) {
            setError("Add at least one option with a unique name.");
            return;
          }
          const { data, error } = await api.POST(
            "/v1/workspaces/{ws}/catalog/option-sets",
            {
              params: { path: { ws } },
              body: { name: `${name.trim()} choices`, options: choices },
            },
          );
          if (error) {
            setError(errorMessage(error));
            return;
          }
          if (!data || !("key" in data)) return;
          set = data.key;
          setCreatedSet(set);
        }
        const multi =
          [
            "text",
            "select",
            "email",
            "phone",
            "url",
            "record_reference",
            "actor_reference",
          ].includes(valueType) && multiple;
        const { data, error } = await api.POST(
          "/v1/workspaces/{ws}/catalog/types/{type}/fields",
          {
            params: { path: { ws, type } },
            body: {
              label: name.trim(),
              value_type: valueType,
              multiple: multi,
              filterable: true,
              ...(choice ? { option_set: set! } : {}),
              ...(valueType === "record_reference"
                ? { target_type: target }
                : {}),
            },
          },
        );
        if (error) {
          setError(errorMessage(error));
          return;
        }
        if (!data || !("key" in data)) return;
      }
      toast(
        mode === "object"
          ? "Object created. Add its attributes to get started."
          : "Attribute created.",
      );
      onSaved();
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
            <DialogTitle>
              {mode === "object" ? "New object" : "New attribute"}
            </DialogTitle>
            <DialogDescription>
              {mode === "object"
                ? "Track a new kind of relationship, such as contracts or subscriptions."
                : "Choose the right field type so your team never needs to guess what to enter."}
            </DialogDescription>
          </DialogHeader>
          <label className="block text-sm">
            Name
            <input
              required
              autoFocus
              maxLength={100}
              className={`${field} mt-1 w-full`}
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={mode === "object" ? "Contract" : "Account manager"}
            />
          </label>
          {mode === "object" ? (
            <label className="block text-sm">
              Plural name
              <input
                className={`${field} mt-1 w-full`}
                value={plural}
                onChange={(e) => setPlural(e.target.value)}
                placeholder="Contracts"
              />
            </label>
          ) : (
            <>
              <label className="block text-sm">
                Field type
                <select
                  className={`${field} mt-1 w-full`}
                  value={valueType}
                  disabled={!!createdSet}
                  onChange={(e) =>
                    setValueType(e.target.value as typeof valueType)
                  }
                >
                  {[
                    "text",
                    "number",
                    "currency",
                    "date",
                    "timestamp",
                    "checkbox",
                    "select",
                    "status",
                    "rating",
                    "email",
                    "phone",
                    "url",
                    "location",
                    "record_reference",
                    "actor_reference",
                  ].map((v) => (
                    <option key={v} value={v}>
                      {v === "record_reference"
                        ? "Relationship to a record"
                        : v === "actor_reference"
                          ? "Workspace member"
                          : v.replaceAll("_", " ")}
                    </option>
                  ))}
                </select>
              </label>
              {valueType === "record_reference" && (
                <label className="block text-sm">
                  Related object
                  <select
                    className={`${field} mt-1 w-full`}
                    value={target}
                    onChange={(e) => setTarget(e.target.value)}
                  >
                    {cat.catalog?.types
                      .filter((t) => !t.archived)
                      .map((t) => (
                        <option key={t.key} value={t.key}>
                          {t.plural}
                        </option>
                      ))}
                  </select>
                </label>
              )}
              {choice && (
                <>
                  <label className="block text-sm">
                    Choices
                    <select
                      className={`${field} mt-1 w-full`}
                      disabled={!!createdSet}
                      value={optionSet}
                      onChange={(e) => setOptionSet(e.target.value)}
                    >
                      <option value="">Create new choices</option>
                      {cat.catalog?.option_sets.map((s) => (
                        <option key={s.key} value={s.key}>
                          {s.name}
                        </option>
                      ))}
                    </select>
                  </label>
                  {!optionSet && (
                    <label className="block text-sm">
                      One option per line
                      <textarea
                        required
                        disabled={!!createdSet}
                        className={`${field} mt-1 min-h-24 w-full`}
                        value={options}
                        onChange={(e) => setOptions(e.target.value)}
                        placeholder={"New\nIn progress\nComplete"}
                      />
                    </label>
                  )}
                </>
              )}
              {[
                "text",
                "select",
                "email",
                "phone",
                "url",
                "record_reference",
                "actor_reference",
              ].includes(valueType) && (
                <label className="flex items-center gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={multiple}
                    onChange={(e) => setMultiple(e.target.checked)}
                  />
                  Allow multiple values
                </label>
              )}
            </>
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
              {busy ? "Creating…" : "Create"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function OptionEditor({
  ws,
  optionSet,
  onClose,
  onSaved,
}: {
  ws: string;
  optionSet: Schemas["OptionSet"];
  onClose: () => void;
  onSaved: () => void;
}) {
  const [items, setItems] = useState(optionSet.options.map((o) => ({ ...o })));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function save(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    const options = items.map((o) => ({
      ...o,
      key:
        o.key ||
        o.label
          .trim()
          .toLowerCase()
          .replace(/[^a-z0-9]+/g, "_")
          .replace(/^_+|_+$/g, "")
          .slice(0, 63),
      label: o.label.trim(),
    }));
    if (
      options.some((o) => !o.key || !o.label) ||
      new Set(options.map((o) => o.key)).size !== options.length
    ) {
      setError("Give each choice a unique, non-empty name.");
      return;
    }
    setBusy(true);
    setError("");
    try {
      const { data, error } = await api.PATCH(
        "/v1/workspaces/{ws}/catalog/option-sets/{set}",
        { params: { path: { ws, set: optionSet.key } }, body: { options } },
      );
      if (error) {
        setError(errorMessage(error));
        return;
      }
      if (data && "key" in data) {
        toast("Choices updated. Archived values remain on existing records.");
        onSaved();
      }
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  function move(index: number, delta: number) {
    setItems((prev) => {
      const next = [...prev];
      const target = index + delta;
      if (target < 0 || target >= next.length) return prev;
      [next[index], next[target]] = [next[target], next[index]];
      return next;
    });
  }
  return (
    <Dialog open onOpenChange={(v) => !v && !busy && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <form className="space-y-4" onSubmit={save}>
          <DialogHeader>
            <DialogTitle>Edit {optionSet.name.toLowerCase()}</DialogTitle>
            <DialogDescription>
              Reorder choices or rename them. Turn off a choice to archive it;
              existing record values are preserved.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            {items.map((item, index) => (
              <div
                key={item.key || `new-${index}`}
                className="flex items-center gap-2"
              >
                <input
                  aria-label={`Choice ${index + 1}`}
                  className={`${field} min-w-0 flex-1`}
                  maxLength={80}
                  required
                  value={item.label}
                  onChange={(e) =>
                    setItems((prev) =>
                      prev.map((v, i) =>
                        i === index ? { ...v, label: e.target.value } : v,
                      ),
                    )
                  }
                />
                <label className="flex items-center gap-1 text-xs">
                  <input
                    type="checkbox"
                    checked={!item.archived}
                    onChange={(e) =>
                      setItems((prev) =>
                        prev.map((v, i) =>
                          i === index
                            ? { ...v, archived: !e.target.checked }
                            : v,
                        ),
                      )
                    }
                  />
                  Available
                </label>
                <button
                  type="button"
                  aria-label={`Move choice ${index + 1} up`}
                  disabled={index === 0}
                  className="rounded border px-2 py-1 disabled:opacity-30"
                  onClick={() => move(index, -1)}
                >
                  ↑
                </button>
                <button
                  type="button"
                  aria-label={`Move choice ${index + 1} down`}
                  disabled={index === items.length - 1}
                  className="rounded border px-2 py-1 disabled:opacity-30"
                  onClick={() => move(index, 1)}
                >
                  ↓
                </button>
              </div>
            ))}
          </div>
          <Button
            type="button"
            variant="ghost"
            onClick={() =>
              setItems((prev) => [
                ...prev,
                { key: "", label: "", archived: false },
              ])
            }
          >
            <Plus className="size-3.5" />
            Add choice
          </Button>
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
            <Button disabled={busy}>{busy ? "Saving…" : "Save choices"}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
function AttributeEditor({
  ws,
  type,
  attribute,
  onClose,
  onSaved,
}: {
  ws: string;
  type: string;
  attribute: Schemas["Field"];
  onClose: () => void;
  onSaved: () => void;
}) {
  const [label, setLabel] = useState(attribute.label);
  const [description, setDescription] = useState(attribute.description);
  const [archived, setArchived] = useState(attribute.archived);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function save(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      const { data, error } = await api.PATCH(
        "/v1/workspaces/{ws}/catalog/types/{type}/fields/{field}",
        {
          params: { path: { ws, type, field: attribute.key } },
          body: { label: label.trim(), description, archived },
        },
      );
      if (error) {
        setError(errorMessage(error));
        return;
      }
      if (data && "key" in data) {
        toast("Attribute updated.");
        onSaved();
      }
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
            <DialogTitle>Edit attribute</DialogTitle>
            <DialogDescription>
              The field's type and relationships stay fixed so existing data
              keeps its meaning.
            </DialogDescription>
          </DialogHeader>
          <label className="block text-sm">
            Label
            <input
              required
              maxLength={100}
              className={`${field} mt-1 w-full`}
              value={label}
              onChange={(e) => setLabel(e.target.value)}
            />
          </label>
          <label className="block text-sm">
            Description
            <textarea
              maxLength={1000}
              className={`${field} mt-1 w-full`}
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </label>
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={archived}
              onChange={(e) => setArchived(e.target.checked)}
            />
            Archive this attribute
          </label>
          <p className="text-xs text-muted-foreground">
            Archiving hides the attribute without deleting its saved values.
          </p>
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
            <Button disabled={busy || !label.trim()}>
              {busy ? "Saving…" : "Save attribute"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
