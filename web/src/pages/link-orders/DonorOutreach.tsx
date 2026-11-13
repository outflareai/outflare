import { useEffect, useState } from "react";
import type { components } from "@outflare/sdk";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { api, errorMessage } from "../../api";
import { Badge, Button, ErrorText, field, toast } from "../../ui";
import { idemp, type LinkOrderDonor } from "./shared";

type Contacts = components["schemas"]["LinkOrderDonorContacts"];
type Contact = components["schemas"]["LinkOrderDonorContact"];
type Outcome = components["schemas"]["LinkOrderDonorOutreach"];
type Sequence = { id: string; name: string; status: string };

const sourceLabel: Record<Contact["source"], string> = {
  person: "Person at the site",
  link_check: "Found by a link-building check",
  lead: "Outreach lead",
  notes: "Donor notes",
};

const outcomeText: Record<Outcome["result"], (o: Outcome) => string> = {
  enrolled: (o) =>
    o.sequence_status === "active"
      ? `Added ${o.email} to ${o.sequence_name}. It sends on the sequence's schedule.`
      : `Added ${o.email} to ${o.sequence_name}. The sequence is ${o.sequence_status}, so nothing is sent until it is started.`,
  already_enrolled: (o) => `${o.email} is already in ${o.sequence_name}.`,
  suppressed: (o) => `${o.email} is on the suppression list, so it was not added.`,
  excluded: (o) => `${o.email} is left out by ${o.sequence_name}'s exclusions, so it was not added.`,
  active_elsewhere: (o) => `${o.email} is active in another sequence, so it was not added.`,
};

function useContacts(brand: string, donor: LinkOrderDonor | undefined) {
  const [contacts, setContacts] = useState<Contacts | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    setContacts(null);
    setErr("");
    if (!donor) return;
    let gone = false;
    void api.GET("/v1/brands/{brand}/link-orders/catalog/items/{id}/contacts", { params: { path: { brand, id: donor.id } } }).then(({ data, error }) => {
      if (gone) return;
      if (error || !data) setErr(errorMessage(error));
      else setContacts(data);
    });
    return () => {
      gone = true;
    };
  }, [brand, donor]);
  return { contacts, err };
}

function ContactRow({ c, onSend }: { c: Contact; onSend?: (c: Contact) => void }) {
  return (
    <li className="flex flex-wrap items-center justify-between gap-2 border-b py-2 last:border-0">
      <div className="flex min-w-0 flex-col">
        <span className="font-medium break-all">{c.email}</span>
        <span className="text-[12px] text-muted-foreground">
          {[c.name, sourceLabel[c.source], c.at_domain ? "" : "not at the site's domain"].filter(Boolean).join(" · ")}
        </span>
      </div>
      <div className="flex flex-wrap items-center gap-1">
        {c.suppressed ? <Badge tone="red">Suppressed</Badge> : null}
        {c.contacted ? <Badge tone="blue">Contacted</Badge> : null}
        {onSend && !c.suppressed ? <Button variant="ghost" onClick={() => onSend(c)}>Send to a sequence</Button> : null}
      </div>
    </li>
  );
}

/** "Find email": the addresses already stored for the donor's site. */
export function FindEmailDialog({ brand, donor, canSend, onClose, onSend }: {
  brand: string;
  donor?: LinkOrderDonor;
  canSend: boolean;
  onClose: () => void;
  onSend: (d: LinkOrderDonor, email: string, name: string) => void;
}) {
  const { contacts, err } = useContacts(brand, donor);
  return (
    <Dialog open={!!donor} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Email for {donor?.domain}</DialogTitle>
          <DialogDescription>
            Addresses already stored in Outflare. This looks only at stored data; it doesn't search the web or use a paid provider.
          </DialogDescription>
        </DialogHeader>
        {err ? <ErrorText>{err}</ErrorText> : null}
        {!contacts && !err ? <p className="text-[13px] text-muted-foreground" role="status">Looking…</p> : null}
        {contacts && contacts.contacts.length ? (
          <ul className="text-[13px]" aria-label="Stored email addresses">
            {contacts.contacts.map((c) => (
              <ContactRow key={c.email} c={c} onSend={canSend && donor ? (x) => onSend(donor, x.email, x.name) : undefined} />
            ))}
          </ul>
        ) : null}
        {contacts && !contacts.contacts.length ? (
          <div className="flex flex-col gap-2 text-[13px]" role="status">
            <p>No email is stored for {contacts.domain}.</p>
            <p className="text-muted-foreground">
              Looked through {contacts.searched.join(", ")}. Type an address when you send the site to a sequence, or add the site as a link prospect and check it in Link building to read its contact pages.
            </p>
          </div>
        ) : null}
        <div className="flex flex-wrap gap-2">
          {canSend && donor ? <Button variant="ghost" onClick={() => onSend(donor, "", "")}>Type an address</Button> : null}
          <Button variant="ghost" onClick={onClose}>Close</Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}

/** "Send to a sequence": add a stored or typed address to one of the brand's sequences. */
export function SendToSequenceDialog({ brand, target, onClose }: {
  brand: string;
  target?: { donor: LinkOrderDonor; email: string; name: string };
  onClose: () => void;
}) {
  const donor = target?.donor;
  const { contacts } = useContacts(brand, donor);
  const [sequences, setSequences] = useState<Sequence[] | null>(null);
  const [seqErr, setSeqErr] = useState("");
  const [seq, setSeq] = useState("");
  const [email, setEmail] = useState("");
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [outcome, setOutcome] = useState<Outcome | null>(null);

  useEffect(() => {
    setEmail(target?.email ?? "");
    setName(target?.name ?? "");
    setErr("");
    setOutcome(null);
  }, [target]);

  // A stored address at the site is the default when none was picked.
  useEffect(() => {
    if (!target || target.email || !contacts) return;
    const first = contacts.contacts.find((c) => !c.suppressed);
    if (first) {
      setEmail(first.email);
      setName(first.name);
    }
  }, [contacts, target]);

  useEffect(() => {
    if (!donor) return;
    let gone = false;
    setSeqErr("");
    void api.GET("/v1/brands/{brand}/sequences", { params: { path: { brand } } }).then(({ data, error }) => {
      if (gone) return;
      if (error || !data) {
        setSeqErr(errorMessage(error));
        setSequences([]);
        return;
      }
      // Platform campaigns' mirrors can't take leads from here.
      const list = data.data.filter((s) => s.status !== "archived" && !(s.links?.length));
      setSequences(list.map((s) => ({ id: s.id, name: s.name, status: s.status })));
      setSeq((cur) => cur || list.find((s) => s.status === "active")?.id || list[0]?.id || "");
    });
    return () => {
      gone = true;
    };
  }, [brand, donor]);

  const chosen = sequences?.find((s) => s.id === seq);
  const send = async () => {
    if (!donor) return;
    const addr = email.trim();
    if (!addr) return setErr("Pick or type an email address.");
    if (!seq) return setErr("Pick a sequence.");
    setBusy(true);
    setErr("");
    const { data, error } = await api.POST("/v1/brands/{brand}/link-orders/catalog/items/{id}/outreach", {
      params: { path: { brand, id: donor.id }, header: idemp() },
      body: { sequence_id: seq, email: addr, name: name.trim() || undefined },
    });
    setBusy(false);
    if (error || !data) return setErr(errorMessage(error));
    setOutcome(data);
    toast(outcomeText[data.result](data), data.result === "enrolled" || data.result === "already_enrolled" ? "ok" : "error");
  };

  const stored = contacts?.contacts.filter((c) => !c.suppressed) ?? [];
  return (
    <Dialog open={!!donor} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Send {donor?.domain} to a sequence</DialogTitle>
          <DialogDescription>
            The address becomes a lead of this brand and joins the sequence with Outreach's usual rules: suppressed, excluded and already-enrolled addresses are skipped. The sequence sends from its own mailboxes on its own schedule.
          </DialogDescription>
        </DialogHeader>
        {outcome ? (
          <div className="flex flex-col gap-3 text-[13px]" role="status">
            <p>{outcomeText[outcome.result](outcome)}</p>
            <div className="flex gap-2">
              <Button onClick={onClose}>Done</Button>
            </div>
          </div>
        ) : (
          <form className="flex flex-col gap-3" onSubmit={(e) => { e.preventDefault(); void send(); }}>
            <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
              Sequence
              <select className={field} value={seq} onChange={(e) => setSeq(e.target.value)} disabled={!sequences?.length}>
                {sequences?.map((s) => <option key={s.id} value={s.id}>{s.name}{s.status === "active" ? "" : ` (${s.status})`}</option>)}
              </select>
            </label>
            {seqErr ? <ErrorText>{seqErr}</ErrorText> : null}
            {sequences && !sequences.length && !seqErr ? (
              <p className="text-[12px] text-muted-foreground">This brand has no sequence yet. Create one in Outreach first.</p>
            ) : null}
            {chosen && chosen.status !== "active" ? (
              <p className="text-[12px] text-muted-foreground">This sequence is {chosen.status}: nothing is sent until it is started.</p>
            ) : null}
            {stored.length ? (
              <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
                Stored address
                <select className={field} value={stored.some((c) => c.email === email) ? email : ""} onChange={(e) => {
                  const c = stored.find((x) => x.email === e.target.value);
                  setEmail(c?.email ?? "");
                  setName(c?.name ?? "");
                }}>
                  <option value="">Type an address</option>
                  {stored.map((c) => <option key={c.email} value={c.email}>{c.email}{c.at_domain ? "" : " (not at the site)"}</option>)}
                </select>
              </label>
            ) : contacts ? (
              <p className="text-[12px] text-muted-foreground">No email is stored for {donor?.domain}. Type one.</p>
            ) : null}
            <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
              Email
              <input className={field} type="email" required value={email} onChange={(e) => setEmail(e.target.value)} placeholder={`editor@${donor?.domain ?? "example.com"}`} />
            </label>
            <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
              Name (optional)
              <input className={field} value={name} maxLength={200} onChange={(e) => setName(e.target.value)} />
            </label>
            {err ? <ErrorText>{err}</ErrorText> : null}
            <div className="flex gap-2">
              <Button type="submit" disabled={busy || !seq || !email.trim()}>{busy ? "Adding…" : "Add to sequence"}</Button>
              <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            </div>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}
