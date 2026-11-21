// Holdout tests: the brand keeps its ads out of some countries for a
// while, and Outflare compares sign-ups there with everywhere else, before
// and during, to say what the ads were really adding. Outflare only
// counts: the countries are excluded on the campaigns by the brand.

import { useCallback, useEffect, useState } from "react";
import type { components } from "@outflare/sdk";
import { Check, ChevronsUpDown, X } from "lucide-react";
import { DataTable } from "@/components/data-table";
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { api, errorMessage } from "../../api";
import { track } from "../../lib/analytics";
import { ask, Badge, Button, Card, ErrorText, Loading, Menu, Status, toast } from "../../ui";
import { platformName } from "../Ads";

type Holdout = components["schemas"]["AdHoldout"];
type Verdict = Holdout["result"]["verdict"];

const codes = "AD AE AF AG AL AM AO AR AT AU AZ BA BB BD BE BF BG BH BI BJ BN BO BR BS BT BW BY BZ CA CD CF CG CH CI CL CM CN CO CR CU CV CY CZ DE DJ DK DM DO DZ EC EE EG ER ES ET FI FJ FR GA GB GD GE GH GM GN GQ GR GT GW GY HK HN HR HT HU ID IE IL IN IQ IR IS IT JM JO JP KE KG KH KM KR KW KZ LA LB LI LK LR LS LT LU LV LY MA MC MD ME MG MK ML MM MN MO MR MT MU MV MW MX MY MZ NA NE NG NI NL NO NP NZ OM PA PE PG PH PK PL PR PT PY QA RO RS RU RW SA SC SD SE SG SI SK SL SN SO SR SV SY SZ TD TG TH TJ TM TN TR TT TW TZ UA UG US UY UZ VE VN YE ZA ZM ZW".split(" ");
const names = new Intl.DisplayNames(["en"], { type: "region" });
const country = (c: string) => names.of(c) ?? c;
const countries = codes.map((c) => ({ code: c, name: country(c) })).sort((a, b) => a.name.localeCompare(b.name));

const statusWord = { scheduled: "Scheduled", running: "Running", finished: "Finished" } as const;
const statusTone = { scheduled: "grey", running: "blue", finished: "green" } as const;
const verdicts: Record<Verdict, { tone: "green" | "amber" | "red" | "grey"; word: string }> = {
  scheduled: { tone: "grey", word: "Not started" },
  not_enough_data: { tone: "grey", word: "Too few sign-ups to say" },
  leaked: { tone: "red", word: "Ads still showing there" },
  ads_add: { tone: "green", word: "The ads add sign-ups" },
  ads_add_little: { tone: "amber", word: "The ads add little" },
  no_difference: { tone: "amber", word: "No difference found" },
};

const round = (v: number) => String(Math.round(v));

function explain(h: Holdout): string {
  const r = h.result;
  switch (r.verdict) {
    case "scheduled":
      return `Starts ${h.starts_on}. Exclude these countries on your campaigns from that day.`;
    case "not_enough_data":
      return `These countries had ${r.held_before.signups} sign-ups in the ${r.days} days before. It takes at least 30 there and 30 elsewhere to compare.`;
    case "leaked":
      return `${r.held_during.paid_visits} visits from paid clicks still came from these countries. Check they are excluded on every campaign.`;
    case "no_difference":
      return `With the ads off, these countries had ${r.held_during.signups} sign-ups against ${round(r.expected_signups ?? 0)} expected: within what chance explains.`;
    default: {
      const part = r.incrementality == null ? "" : ` That is ${Math.round(r.incrementality * 100)}% of the ${r.held_before.ad_signups} that ads were credited with there before.`;
      return `With the ads off, these countries had ${r.held_during.signups} sign-ups against ${round(r.expected_signups ?? 0)} expected: the ads were adding about ${round(r.lift_signups ?? 0)}.${part}`;
    }
  }
}

function CountryPicker({ value, onChange }: { value: string[]; onChange: (v: string[]) => void }) {
  const [open, setOpen] = useState(false);
  const toggle = (c: string) => onChange(value.includes(c) ? value.filter((x) => x !== c) : [...value, c]);
  return (
    <div className="space-y-1.5">
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger asChild>
          <Button variant="ghost" data-testid="holdout-countries">
            {value.length === 0 ? "Pick countries" : `${value.length} ${value.length === 1 ? "country" : "countries"}`} <ChevronsUpDown className="size-3.5 opacity-60" />
          </Button>
        </PopoverTrigger>
        <PopoverContent className="w-72 p-0" align="start">
          <Command>
            <CommandInput placeholder="Search countries" />
            <CommandList>
              <CommandEmpty>No country by that name.</CommandEmpty>
              <CommandGroup>
                {countries.map((c) => (
                  <CommandItem key={c.code} value={`${c.name} ${c.code}`} onSelect={() => toggle(c.code)}>
                    <Check className={value.includes(c.code) ? "size-4" : "size-4 opacity-0"} />
                    {c.name}
                  </CommandItem>
                ))}
              </CommandGroup>
            </CommandList>
          </Command>
        </PopoverContent>
      </Popover>
      {value.length > 0 && (
        <div className="flex flex-wrap gap-1.5">
          {value.map((c) => (
            <button key={c} type="button" onClick={() => toggle(c)} aria-label={`Remove ${country(c)}`} className="inline-flex items-center gap-1 rounded-md border px-2 py-0.5 text-[12px]">
              {country(c)} <X className="size-3 opacity-60" />
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

const iso = (d: Date) => d.toISOString().slice(0, 10);

function NewHoldout({ brand, connectors, done, cancel }: { brand: string; connectors: string[]; done: () => void; cancel: () => void }) {
  const [name, setName] = useState("");
  const [picked, setPicked] = useState<string[]>([]);
  const [connector, setConnector] = useState("");
  const [starts, setStarts] = useState(iso(new Date(Date.now() + 86400000)));
  const [ends, setEnds] = useState(iso(new Date(Date.now() + 28 * 86400000)));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const save = async () => {
    setBusy(true);
    setError("");
    const { error } = await api.POST("/v1/brands/{brand}/ads/holdouts", {
      params: { path: { brand } },
      body: { name: name.trim() || `${picked.map(country).join(", ")} without ads`, countries: picked, starts_on: starts, ends_on: ends, connector },
    });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    track("ui.ads_holdout_saved", { countries: picked.length, connector: connector || "all" });
    toast("Holdout saved. Now exclude these countries on your campaigns for the same days.");
    done();
  };
  const label = "mb-1 block text-[12px] text-muted-foreground";
  return (
    <div className="space-y-4 rounded-md border p-4 text-[13px]" data-testid="holdout-form">
      <ol className="list-decimal space-y-1 pl-5 text-muted-foreground">
        <li>Pick a few countries that together bring a steady share of your sign-ups, not your biggest one.</li>
        <li>Save the holdout here, so Outflare knows what to compare.</li>
        <li>Exclude those countries on every campaign for the same days: on a Google Ads campaign's page here under Places, or in the network itself. Outflare does not change your campaigns.</li>
      </ol>
      <div className="grid gap-4 sm:grid-cols-2">
        <div className="sm:col-span-2">
          <span className={label}>Countries to keep the ads out of</span>
          <CountryPicker value={picked} onChange={setPicked} />
        </div>
        <label>
          <span className={label}>First day without ads</span>
          <Input type="date" value={starts} onChange={(e) => setStarts(e.target.value)} aria-label="First day without ads" />
        </label>
        <label>
          <span className={label}>Last day without ads (7 to 90 days)</span>
          <Input type="date" value={ends} onChange={(e) => setEnds(e.target.value)} aria-label="Last day without ads" />
        </label>
        <label>
          <span className={label}>Which ads are off there</span>
          <select aria-label="Which ads are off there" className="h-9 w-full rounded-md border bg-transparent px-2" value={connector} onChange={(e) => setConnector(e.target.value)}>
            <option value="">Every network's</option>
            {connectors.map((c) => <option key={c} value={c}>Only {platformName(c)}</option>)}
          </select>
        </label>
        <label>
          <span className={label}>Name (optional)</span>
          <Input value={name} maxLength={120} onChange={(e) => setName(e.target.value)} placeholder="Portugal without ads" aria-label="Name" />
        </label>
      </div>
      {error && <ErrorText>{error}</ErrorText>}
      <div className="flex gap-2">
        <Button variant="primary" onClick={save} disabled={busy || picked.length === 0}>Save</Button>
        <Button variant="ghost" onClick={cancel}>Cancel</Button>
      </div>
    </div>
  );
}

export function Holdouts({ brand, connectors, canManage }: { brand: string; connectors: string[]; canManage: boolean }) {
  const [rows, setRows] = useState<Holdout[] | null>(null);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const load = useCallback(() => {
    api.GET("/v1/brands/{brand}/ads/holdouts", { params: { path: { brand } } }).then(({ data, error }) => (error ? setError(errorMessage(error)) : (setError(""), setRows(data.data))));
  }, [brand]);
  useEffect(load, [load]);
  const end = async (h: Holdout) => {
    const { error } = await api.POST("/v1/brands/{brand}/ads/holdouts/{id}/end", { params: { path: { brand, id: h.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast("Holdout ends today. Take the exclusions off your campaigns.");
    load();
  };
  const remove = async (h: Holdout) => {
    const r = await ask({ title: `Delete "${h.name}"?`, body: "Only the holdout's note is deleted. Your campaigns, visits and sign-ups stay as they are.", choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Delete", value: "ok", variant: "danger-solid" }] });
    if (r?.choice !== "ok") return;
    const { error } = await api.DELETE("/v1/brands/{brand}/ads/holdouts/{id}", { params: { path: { brand, id: h.id } } });
    if (error) return toast(errorMessage(error), "error");
    load();
  };
  return (
    <Card
      title="Holdout tests"
      description="What would happen without the ads? Keep them out of a few countries for some weeks, and Outflare compares sign-ups there with everywhere else, before and during. The gap is what the ads really add, whatever any network or click says. Source: your sign-up events and visits by country."
      action={canManage && !adding ? <Button data-testid="holdout-new" onClick={() => setAdding(true)}>New holdout</Button> : undefined}
    >
      <div className="space-y-3">
        {adding && <NewHoldout brand={brand} connectors={connectors} cancel={() => setAdding(false)} done={() => { setAdding(false); load(); }} />}
        {error && <ErrorText>{error}</ErrorText>}
        {!rows && !error && <Loading />}
        {rows && (
          <DataTable
            data={rows}
            rowId={(h) => h.id}
            columnsMenu={false}
            empty="No holdouts yet."
            columns={[
              { id: "status", header: "Status", accessorFn: (h) => statusWord[h.status], cell: ({ row: { original: h } }) => <Status tone={statusTone[h.status]}>{statusWord[h.status]}</Status> },
              { id: "name", header: "Holdout", accessorFn: (h) => h.name, cell: ({ row: { original: h } }) => (
                <div className="max-w-xs whitespace-normal">
                  <div>{h.name}</div>
                  <div className="text-[12px] text-muted-foreground">{h.countries.map(country).join(", ")} · {h.connector ? platformName(h.connector) : "every network"}</div>
                </div>
              ) },
              { id: "days", header: "Days", accessorFn: (h) => h.starts_on, cell: ({ row: { original: h } }) => <span className="whitespace-nowrap text-muted-foreground">{h.starts_on} to {h.ends_on}</span> },
              { id: "signups", header: "Sign-ups there", enableSorting: false, cell: ({ row: { original: h } }) => h.result.verdict === "scheduled" ? <span className="text-muted-foreground">—</span> : (
                <div className="whitespace-nowrap font-mono tabular-nums">
                  {h.result.held_during.signups} <span className="font-sans text-[12px] text-muted-foreground">without ads · {h.result.expected_signups == null ? h.result.held_before.signups : round(h.result.expected_signups)} {h.result.expected_signups == null ? "before" : "expected"}</span>
                </div>
              ) },
              { id: "result", header: "Result", accessorFn: (h) => verdicts[h.result.verdict].word, cell: ({ row: { original: h } }) => (
                <div className="max-w-md space-y-1 whitespace-normal" data-testid={`holdout-result-${h.id}`}>
                  <Badge tone={verdicts[h.result.verdict].tone}>{verdicts[h.result.verdict].word}</Badge>
                  <div className="text-[12px] text-muted-foreground">{explain(h)}</div>
                </div>
              ) },
              ...(canManage
                ? [{ id: "more", header: "", enableSorting: false, cell: ({ row: { original: h } }: { row: { original: Holdout } }) => (
                    <div className="flex justify-end">
                      <Menu aria={`More for ${h.name}`} items={[h.status === "running" && h.ends_on > iso(new Date()) && { label: "End today", onClick: () => end(h) }, { label: "Delete", danger: true, onClick: () => remove(h) }]} />
                    </div>
                  ) }]
                : []),
            ]}
          />
        )}
      </div>
    </Card>
  );
}
