import { useState } from "react";
import { api, errorMessage } from "@/api";
import { ErrorText, Status, toast } from "@/ui";
import { setPeopleSetup, usePeopleSetup, type PeopleSetup } from "@/lib/peopleSetup";

const sellsToChoices: { key: PeopleSetup["sells_to"]; label: string; hint: string }[] = [
  { key: "consumers", label: "Consumers", hint: "A customer is a person. Companies is hidden." },
  { key: "companies", label: "Companies", hint: "A customer is a company. A work email suggests it." },
  { key: "mixed", label: "Both", hint: "People with no company show as Individual. Free-mail sign-ups stay Individual." },
  { key: "marketplace", label: "Marketplace", hint: "People carry a Buyer or Seller label." },
];

// How the brand's People fit the way it sells: who a customer is, and
// whether its product has accounts (workspaces, teams) people belong to.
// Both start at the business type's defaults and say so.
export function PeopleSetupCard({ brandId, canManage }: { brandId: string; canManage: boolean }) {
  const setup = usePeopleSetup(brandId || undefined);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  if (!setup) {
    return <p className="rounded-lg border p-4 text-sm text-muted-foreground">Checking how People are set up…</p>;
  }
  const save = async (body: { sells_to?: PeopleSetup["sells_to"] | null; accounts?: boolean | null }, what: string) => {
    setBusy(what);
    setError("");
    const { data, error } = await api.PATCH("/v1/brands/{brand}/people-setup", { params: { path: { brand: brandId } }, body });
    setBusy("");
    if (!data) {
      setError(errorMessage(error));
      return;
    }
    setPeopleSetup(brandId, data);
    toast("People setup saved");
  };
  const fromModel = (isDefault: boolean) =>
    isDefault ? <span className="text-xs"><Status tone="grey">Default for your business type</Status></span> : <span className="text-xs"><Status tone="green">Set by your team</Status></span>;
  return (
    <section className="space-y-4 rounded-lg border p-4" aria-labelledby="people-setup">
      <div>
        <h2 id="people-setup" className="font-semibold">People setup</h2>
        <p className="mt-1 text-sm text-muted-foreground">
          How People, Companies and Accounts fit the way this brand sells. The CRM menu shows {setup.lists.map(cap).join(", ")}.
        </p>
      </div>
      <div className="space-y-2">
        <div className="flex flex-wrap items-center gap-2">
          <h3 className="text-sm font-medium">Who do you sell to?</h3>
          {fromModel(setup.sells_to_default)}
          {!setup.sells_to_default && canManage && (
            <button className="text-xs text-muted-foreground underline hover:text-foreground" disabled={!!busy} onClick={() => save({ sells_to: null }, "sells_to")}>
              Use the default
            </button>
          )}
        </div>
        <div role="radiogroup" aria-label="Who do you sell to?" className="grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
          {sellsToChoices.map((c) => {
            const on = setup.sells_to === c.key;
            return (
              <button
                key={c.key}
                role="radio"
                aria-checked={on}
                disabled={!canManage || !!busy}
                onClick={() => !on && save({ sells_to: c.key }, "sells_to")}
                className={`rounded-lg border p-3 text-left text-sm disabled:cursor-default ${on ? "border-brand bg-brand/5" : "hover:bg-muted/50"}`}
              >
                <span className="flex items-center gap-2 font-medium">
                  <span aria-hidden="true" className={`size-3 rounded-full border ${on ? "border-brand bg-brand" : ""}`} />
                  {c.label}
                </span>
                <span className="mt-1 block text-xs text-muted-foreground">{c.hint}</span>
              </button>
            );
          })}
        </div>
      </div>
      <div className="space-y-2">
        <div className="flex flex-wrap items-center gap-2">
          <h3 className="text-sm font-medium">Does your product have accounts?</h3>
          {fromModel(setup.accounts_default)}
          {!setup.accounts_default && canManage && (
            <button className="text-xs text-muted-foreground underline hover:text-foreground" disabled={!!busy} onClick={() => save({ accounts: null }, "accounts")}>
              Use the default
            </button>
          )}
        </div>
        <label className="flex items-start gap-2 text-sm">
          <input
            type="checkbox"
            className="mt-0.5"
            checked={setup.accounts}
            disabled={!canManage || !!busy}
            onChange={(e) => save({ accounts: e.target.checked }, "accounts")}
          />
          <span>
            Yes: people sign up into workspaces or teams.
            <span className="block text-xs text-muted-foreground">
              Each one becomes an Account, with its plan, usage, billing and members. Being in an account never sets a person's company.
            </span>
          </span>
        </label>
      </div>
      {!canManage && <p className="text-xs text-muted-foreground">A workspace administrator can change these.</p>}
      <ErrorText>{error}</ErrorText>
    </section>
  );
}

const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);
