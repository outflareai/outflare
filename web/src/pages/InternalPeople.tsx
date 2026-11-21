import { PickBrand } from "@/components/pick-brand";
import { pickedBrandId, useBrand } from "@/lib/brand";
// Internal and test people: the brand's own team (by email domain) and its
// test accounts (by email address), left out of funnels, trends, retention,
// stickiness, lifecycle and paths while the switch is on. One switch, two
// lists, and a count of the people they match.

import { type FormEvent, useCallback, useEffect, useState } from "react";
import type { paths } from "@outflare/sdk";
import { Switch } from "@/components/ui/switch";
import { api, errorMessage } from "../api";
import { Button, ErrorText, Loading, PageHeader, Stat, Stats, toast } from "../ui";

type Props = { ws: string; canManage: boolean };
type Setting = paths["/v1/brands/{brand}/internal-people"]["get"]["responses"]["200"]["content"]["application/json"];

const lines = (text: string) => [...new Set(text.split(/[\n,]/).map((x) => x.trim()).filter(Boolean))];
const count = (n: number, one: string, many = `${one}s`) => `${n.toLocaleString()} ${n === 1 ? one : many}`;
const area = "min-h-28 w-full rounded-md border border-input bg-transparent px-2.5 py-1.5 font-mono text-[13px]";

export function InternalPeople(props: Props) {
  const { brand, active } = useBrand(props.ws);
  if (!brand) return <PickBrand what="Internal and test people" brands={active.length} />;
  return <Settings key={brand.id} {...props} />;
}

function Settings({ canManage }: Props) {
  const [saved, setSaved] = useState<Setting | null>(null);
  const [enabled, setEnabled] = useState(false);
  const [domains, setDomains] = useState("");
  const [emails, setEmails] = useState("");
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);

  const show = useCallback((s: Setting) => {
    setSaved(s);
    setEnabled(s.enabled);
    setDomains(s.domains.join("\n"));
    setEmails(s.emails.join("\n"));
  }, []);
  useEffect(() => {
    api.GET("/v1/brands/{brand}/internal-people", { params: { path: { brand: pickedBrandId() } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      show(data);
    });
  }, [show]);

  const put = async (next: { enabled: boolean; domains: string[]; emails: string[] }, done: string) => {
    setSaving(true);
    const { data, error } = await api.PUT("/v1/brands/{brand}/internal-people", { params: { path: { brand: pickedBrandId() } }, body: next });
    setSaving(false);
    if (error || !data) return setError(errorMessage(error));
    setError("");
    show(data);
    toast(done);
  };
  const save = (e: FormEvent) => {
    e.preventDefault();
    void put({ enabled, domains: lines(domains), emails: lines(emails) }, "Saved");
  };
  // The switch saves at once, with the lists as last saved.
  const flip = (on: boolean) => {
    if (!saved) return;
    setEnabled(on);
    void put({ enabled: on, domains: saved.domains, emails: saved.emails }, on ? "Reports now leave them out" : "Reports count everyone again");
  };

  const header = (
    <PageHeader
      title="Internal and test people"
      description="Leave your own team and your test accounts out of funnels, trends, retention, stickiness, lifecycle and paths, so the numbers show customers only."
    />
  );
  if (!saved) return <div>{header}{error ? <ErrorText>{error}</ErrorText> : <Loading />}</div>;
  const empty = saved.domains.length + saved.emails.length === 0;
  const changed = lines(domains).join("\n") !== saved.domains.join("\n") || lines(emails).join("\n") !== saved.emails.join("\n");
  return (
    <div className="space-y-6">
      {header}
      <Stats cols={3}>
        <Stat label="Left out of reports" value={saved.enabled ? "On" : "Off"} tone={saved.enabled ? "green" : "amber"} />
        <Stat label="People matched" value={saved.matched.toLocaleString()} sub={saved.enabled ? "Not counted in reports" : "Counted until you turn this on"} />
        <Stat label="On the list" value={(saved.domains.length + saved.emails.length).toLocaleString()} sub={`${count(saved.domains.length, "domain")}, ${count(saved.emails.length, "address", "addresses")}`} />
      </Stats>

      <label className="flex items-start gap-3 text-[13px]">
        <Switch checked={enabled} disabled={!canManage || saving} onCheckedChange={flip} className="mt-0.5" aria-label="Leave internal and test people out of reports" />
        <span>
          <span className="font-medium">Leave them out of reports.</span>{" "}
          <span className="text-muted-foreground">
            {empty ? "Add a domain or an address below first: with an empty list, nobody is left out." : "Nothing is deleted. Turn it off and they are counted again."}
          </span>
        </span>
      </label>

      <form onSubmit={save} className="max-w-2xl space-y-4">
        <label className="block space-y-1 text-[13px]">
          <span className="font-medium">Your team's email domains</span>
          <textarea className={area} value={domains} disabled={!canManage} onChange={(e) => setDomains(e.target.value)} placeholder="your-company.example" aria-label="Email domains" />
          <span className="text-muted-foreground">One per line. Everyone with an email address at one of these is left out.</span>
        </label>
        <label className="block space-y-1 text-[13px]">
          <span className="font-medium">Test accounts</span>
          <textarea className={area} value={emails} disabled={!canManage} onChange={(e) => setEmails(e.target.value)} placeholder="qa@another-domain.example" aria-label="Email addresses" />
          <span className="text-muted-foreground">One email address per line, for single accounts on other domains.</span>
        </label>
        {error && <ErrorText>{error}</ErrorText>}
        {canManage && <Button type="submit" disabled={saving || !changed}>{saving ? "Saving" : "Save list"}</Button>}
        <p className="text-[13px] text-muted-foreground">
          Only people Outflare knows by email are left out. Visits from someone on your team who hasn't signed in can't be told apart from any other visitor.
        </p>
      </form>
    </div>
  );
}
