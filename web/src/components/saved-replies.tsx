import { useCallback, useEffect, useState } from "react";
import type { SavedReply } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { pickedBrandId } from "@/lib/brand";
import { Button, confirmAction, ErrorText, field, Menu, toast } from "../ui";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { cn } from "@/lib/utils";

// Saved replies: answers the brand's team wrote once. The menu sits in the
// reply box of a support conversation and of a reply to outreach; picking
// one drops its text in with {first_name} and {my_name} filled in, and
// "Manage saved replies" adds, edits and deletes them.
export function SavedReplies({ canManage, firstName, myName, draft, onPick }: { canManage: boolean; firstName: string; myName: string; draft: string; onPick: (text: string) => void }) {
  const [list, setList] = useState<SavedReply[]>([]);
  const [manage, setManage] = useState(false);
  const load = useCallback(() => {
    api.GET("/v1/brands/{brand}/saved-replies", { params: { path: { brand: pickedBrandId() } } }).then(({ data }) => setList(data?.data ?? []));
  }, []);
  useEffect(load, [load]);

  function pick(r: SavedReply) {
    onPick(r.body.replaceAll("{first_name}", firstName || "there").replaceAll("{my_name}", myName));
    void api.POST("/v1/brands/{brand}/saved-replies/{id}/used", { params: { path: { brand: pickedBrandId(), id: r.id } } });
  }
  if (!canManage) return null;
  return (
    <>
      <Menu
        label="Saved replies"
        aria="Saved replies"
        items={[...list.map((r) => ({ label: r.name, onClick: () => pick(r) })), { label: list.length ? "Manage saved replies…" : "Add a saved reply…", onClick: () => setManage(true) }]}
      />
      {manage && <ManageSavedReplies list={list} draft={draft} onChange={load} onClose={() => setManage(false)} />}
    </>
  );
}

function ManageSavedReplies({ list, draft, onChange, onClose }: { list: SavedReply[]; draft: string; onChange: () => void; onClose: () => void }) {
  // The one being written: a new one (id "") or an existing one.
  const [edit, setEdit] = useState<{ id: string; name: string; body: string } | null>(list.length ? null : { id: "", name: "", body: draft });
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const brand = pickedBrandId();

  async function save() {
    if (!edit) return;
    setBusy(true);
    const body = { name: edit.name.trim(), body: edit.body };
    const { error } = edit.id
      ? await api.PATCH("/v1/brands/{brand}/saved-replies/{id}", { params: { path: { brand, id: edit.id } }, body })
      : await api.POST("/v1/brands/{brand}/saved-replies", { params: { path: { brand } }, body });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    setError("");
    setEdit(null);
    toast(edit.id ? "Saved reply updated." : "Saved reply added.");
    onChange();
  }
  async function remove(r: SavedReply) {
    if (!(await confirmAction(`Delete the saved reply "${r.name}"?`, "Replies already sent with it keep their text.", "Delete saved reply"))) return;
    const { error } = await api.DELETE("/v1/brands/{brand}/saved-replies/{id}", { params: { path: { brand, id: r.id } } });
    if (error) return setError(errorMessage(error));
    toast("Saved reply deleted.");
    onChange();
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>Saved replies</DialogTitle>
          <DialogDescription>Answers you write once and drop into a reply. Everyone on this brand shares them, in support and in sales replies.</DialogDescription>
        </DialogHeader>
        {edit ? (
          <form
            className="space-y-3"
            onSubmit={(e) => {
              e.preventDefault();
              void save();
            }}
          >
            <label className="block space-y-1 text-sm">
              <span className="font-medium">Name</span>
              <input autoFocus className={cn(field, "w-full")} maxLength={80} placeholder="Pricing, Book a demo, Refund policy…" value={edit.name} onChange={(e) => setEdit({ ...edit, name: e.target.value })} />
            </label>
            <label className="block space-y-1 text-sm">
              <span className="font-medium">Text</span>
              <textarea className={cn(field, "min-h-40 w-full [field-sizing:content]")} placeholder={"Hi {first_name},\n\n…\n\n{my_name}"} value={edit.body} onChange={(e) => setEdit({ ...edit, body: e.target.value })} />
              <span className="block text-xs text-muted-foreground">
                Write <code>{"{first_name}"}</code> for the person you're answering and <code>{"{my_name}"}</code> for whoever sends it. They are filled in when the reply is used.
              </span>
            </label>
            <ErrorText>{error}</ErrorText>
            <div className="flex justify-end gap-2">
              <Button type="button" variant="ghost" onClick={() => (list.length ? setEdit(null) : onClose())}>Cancel</Button>
              <Button disabled={busy || !edit.name.trim() || !edit.body.trim()}>{busy ? "Saving…" : edit.id ? "Save changes" : "Add saved reply"}</Button>
            </div>
          </form>
        ) : (
          <>
            <ul className="max-h-96 divide-y overflow-y-auto text-sm">
              {list.map((r) => (
                <li key={r.id} className="flex items-start gap-3 py-2.5">
                  <div className="min-w-0 flex-1">
                    <p className="font-medium">{r.name}</p>
                    <p className="line-clamp-2 text-[13px] text-muted-foreground">{r.body}</p>
                    <p className="mt-0.5 text-xs text-muted-foreground">{r.uses === 0 ? "Not used yet" : r.uses === 1 ? "Used once" : `Used ${r.uses} times`}</p>
                  </div>
                  <Button type="button" variant="ghost" onClick={() => setEdit({ id: r.id, name: r.name, body: r.body })}>Edit</Button>
                  <Button type="button" variant="ghost" onClick={() => void remove(r)}>Delete</Button>
                </li>
              ))}
            </ul>
            <ErrorText>{error}</ErrorText>
            <div className="flex justify-end">
              <Button type="button" onClick={() => setEdit({ id: "", name: "", body: draft })}>Add a saved reply</Button>
            </div>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}
