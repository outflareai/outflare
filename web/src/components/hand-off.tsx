import { useEffect, useState } from "react";
import type { Schemas, SupportConversation } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { pickedBrandId } from "@/lib/brand";
import { Button, ErrorText, field, toast } from "../ui";
import { Icon } from "../icons";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { cn } from "@/lib/utils";

type Team = { id: string; name: string };
type Signal = Schemas["Signal"];
export type Spotted = Signal;

// salesTeam is the team a sales opportunity goes to by default: the one
// with "sales" in its name.
export function salesTeam(teams: Team[]): Team | undefined {
  return teams.find((t) => /sales/i.test(t.name));
}

// spotted finds the open signal the brand's own AI raised from this
// conversation, if any: the suggestion that it is a sales opportunity.
export function useSpotted(conv: SupportConversation | null): Signal | null {
  const [found, setFound] = useState<Signal | null>(null);
  const id = conv?.id;
  const record = conv?.record_id;
  useEffect(() => {
    setFound(null);
    if (!id || !record) return;
    let live = true;
    api.GET("/v1/brands/{brand}/signals", { params: { path: { brand: pickedBrandId() }, query: { record, status: "open" } as never } }).then(({ data }) => {
      if (!live) return;
      const list = ((data as { data?: Signal[] } | undefined)?.data ?? []) as Signal[];
      setFound(list.find((s) => s.source === "agent" && (s.data as { ref?: string }).ref === id) ?? null);
    });
    return () => {
      live = false;
    };
  }, [id, record]);
  return found;
}

// "Sales opportunity" on a support conversation: hand it to the sales team
// (or any team) in one step. It moves there unassigned, a private note
// says why, you keep it under Watching, and the customer's owner gets it
// in Needs attention.
export function HandOff({ conv, teams, spotted, open, onOpen, onDone }: { conv: SupportConversation; teams: Team[]; spotted: Signal | null; open: boolean; onOpen: (open: boolean) => void; onDone: () => void }) {
  const [team, setTeam] = useState("");
  const [note, setNote] = useState("");
  const [flag, setFlag] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!open) return;
    setTeam(salesTeam(teams)?.id ?? teams.find((t) => t.id !== conv.team?.id)?.id ?? "");
    setNote(spotted ? [spotted.title, spotted.detail].filter(Boolean).join(". ") : "");
    setFlag(!spotted);
    setError("");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  async function send() {
    setBusy(true);
    const { data, error } = await api.POST("/v1/brands/{brand}/support/conversations/{id}/hand-off", {
      params: { path: { brand: pickedBrandId(), id: conv.id } },
      body: { team_id: team, ...(note.trim() ? { note: note.trim() } : {}), ...(flag && conv.record_id ? { signal: "upsell" } : {}) },
    });
    setBusy(false);
    if (error || !data) return setError(errorMessage(error));
    onOpen(false);
    const name = teams.find((t) => t.id === team)?.name ?? "the team";
    toast(`Handed to ${name}. It stays under your Watching view.`);
    onDone();
  }

  return (
    <>
      <Button variant="ghost" onClick={() => onOpen(true)} title="Hand this conversation to your sales team">
        <Icon name="revenue" className="size-3.5" /> Sales opportunity
      </Button>
      <Dialog open={open} onOpenChange={onOpen}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>Hand to sales</DialogTitle>
            <DialogDescription>The conversation moves to the team with nobody assigned, so they pick it up. You keep it under Watching, and the customer never sees a hand-over.</DialogDescription>
          </DialogHeader>
          {teams.length === 0 ? (
            <p className="text-[13px] text-muted-foreground">You have no teams yet. Add a Sales team under Settings, Teams, then hand this over.</p>
          ) : (
            <div className="space-y-3 text-[13px]">
              <label className="block space-y-1">
                <span className="text-xs text-muted-foreground">Team</span>
                <select className={cn(field, "w-full")} value={team} onChange={(e) => setTeam(e.target.value)}>
                  {teams.map((t) => (
                    <option key={t.id} value={t.id}>{t.name}</option>
                  ))}
                </select>
              </label>
              <label className="block space-y-1">
                <span className="text-xs text-muted-foreground">What should they know? (optional, only your team sees it)</span>
                <textarea className={cn(field, "h-auto w-full py-2")} rows={3} maxLength={2000} value={note} onChange={(e) => setNote(e.target.value)} placeholder="Wants ten more seats before March." />
              </label>
              {spotted ? (
                <p className="text-xs text-muted-foreground">Your AI already flagged this for the customer's owner ({spotted.type_label}).</p>
              ) : conv.record_id ? (
                <label className="flex items-start gap-2">
                  <input type="checkbox" className="mt-0.5" checked={flag} onChange={(e) => setFlag(e.target.checked)} />
                  <span>Also put it in Needs attention for the customer's owner</span>
                </label>
              ) : null}
              {error && <ErrorText>{error}</ErrorText>}
              <div className="flex justify-end gap-2">
                <Button variant="ghost" onClick={() => onOpen(false)}>Cancel</Button>
                <Button disabled={busy || !team} onClick={send}>Hand over</Button>
              </div>
            </div>
          )}
        </DialogContent>
      </Dialog>
    </>
  );
}

// The line above the reply box when the brand's AI read the conversation
// and found an opportunity in it.
export function SpottedBanner({ spotted, onHandOff }: { spotted: Signal; onHandOff: () => void }) {
  return (
    <div className="flex items-center gap-2 border-t bg-violet-500/5 px-4 py-2 text-[13px]">
      <Icon name="ai" className="size-3.5 shrink-0 text-violet-700 dark:text-violet-400" />
      <p className="min-w-0 flex-1 truncate">
        <span className="font-medium">Looks like a sales opportunity.</span> <span className="text-muted-foreground">{spotted.type_label}{spotted.title ? `: ${spotted.title}` : ""}</span>
      </p>
      <Button variant="ghost" onClick={onHandOff}>Hand to sales</Button>
    </div>
  );
}
