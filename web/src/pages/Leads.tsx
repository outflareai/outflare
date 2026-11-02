import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent,
} from "react";
import type { Lead, LeadImportResult, LeadList, Schemas } from "@outflare/sdk";
import { PickBrand } from "@/components/pick-brand";
import { api, errorMessage } from "../api";
import {
  ask,
  Badge,
  Button,
  Card,
  Empty,
  ErrorText,
  field,
  FilePicker,
  Loading,
  PageHeader,
  toast,
} from "../ui";
import { Search } from "lucide-react";
import { DataTable } from "@/components/data-table";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { ago } from "../format";
import type { Tab } from "../App";
import { useBrand } from "@/lib/brand";
import { RecordPicker } from "./RecordEdit";
import { identity, displayName } from "@/lib/records";
import { CheckCircle2, Building2, Loader2 } from "lucide-react";
import { RecordPartners } from "./Partners";

// How each verification result reads in the table. Unverified addresses are
// checked automatically before their first email, so they aren't a problem.
const verdicts: Record<
  string,
  { label: string; tone: "green" | "amber" | "red" | "grey"; title: string }
> = {
  deliverable: {
    label: "Valid",
    tone: "green",
    title: "The mailbox exists and accepts mail.",
  },
  risky: {
    label: "Risky",
    tone: "amber",
    title:
      "Catch-all, role or disposable address: it may bounce. Sent unless your verification settings skip risky addresses.",
  },
  undeliverable: {
    label: "Invalid",
    tone: "red",
    title: "The address doesn't exist. It won't be emailed.",
  },
  unknown: {
    label: "Unknown",
    tone: "grey",
    title:
      "The provider couldn't tell. Sent unless your verification settings skip unknown addresses.",
  },
};

function Verdict({ v }: { v: Lead["verification"] }) {
  if (!v)
    return (
      <span
        className="text-muted-foreground"
        title="Checked automatically before the first email."
      >
        Not yet
      </span>
    );
  const d = verdicts[v];
  return (
    <Badge tone={d.tone} title={d.title}>
      {d.label}
    </Badge>
  );
}

// Leads belong to one brand (plans/brands/README.md): with all brands
// picked the page asks for one, and switching brands opens it afresh.
export function Leads(props: { ws: string; canManage: boolean; go: (t: Tab) => void }) {
  const { brand, active } = useBrand(props.ws);
  if (!brand) return <PickBrand what="Leads" brands={active.length} />;
  return <LeadsPage key={brand.id} {...props} />;
}

function LeadsPage({ ws, canManage, go }: { ws: string; canManage: boolean; go: (t: Tab) => void }) {
  const brandId = useBrand(ws).brand?.id ?? "";
  const [leads, setLeads] = useState<Lead[]>([]);
  const [total, setTotal] = useState(0);
  const [next, setNext] = useState<string | null>(null);
  const [lists, setLists] = useState<LeadList[]>([]);
  const [listId, setListId] = useState("");
  const [verification, setVerification] = useState("");
  const [q, setQ] = useState("");
  const [loaded, setLoaded] = useState(false);
  const [importing, setImporting] = useState<boolean | null>(null);
  // The lead open in the form: "new" to add one.
  const [open, setOpen] = useState<Lead | "new" | null>(null);

  const load = useCallback(
    async (cursor?: string) => {
      const filters = { ...(q ? { q } : {}), ...(verification ? { verification: verification as "deliverable" } : {}), limit: 100 };
      // A list's leads come from the list, in the same order.
      const page = async () => {
        if (!listId) {
          const { data } = await api.GET("/v1/brands/{brand}/leads", { params: { path: { brand: brandId }, query: { ...filters, ...(cursor ? { cursor } : {}) } } });
          return data && { data: data.data as Lead[], total: data.total, next: data.next_cursor ?? null };
        }
        const query = { ...filters, sort: "email" as const, ...(cursor ? { cursor } : {}) };
        const { data } = await api.GET("/v1/brands/{brand}/lead-lists/{id}/members", { params: { path: { brand: brandId, id: listId }, query } });
        return data && { data: data.data as Lead[], total: data.total, next: data.next_cursor ?? null };
      };
      const [l, ls] = await Promise.all([page(), cursor ? Promise.resolve(null) : api.GET("/v1/brands/{brand}/lead-lists", { params: { path: { brand: brandId } } })]);
      if (l) {
        setLeads((prev) => (cursor ? [...prev, ...l.data] : l.data));
        setTotal(l.total);
        setNext(l.next);
      }
      if (ls?.data) setLists(ls.data.data);
      setLoaded(true);
    },
    [ws, listId, q, verification, brandId], // eslint-disable-line react-hooks/exhaustive-deps
  );

  useEffect(() => {
    const t = setTimeout(() => load(), 200);
    return () => clearTimeout(t);
  }, [load]);

  async function remove(l: Lead): Promise<boolean> {
    const r = await ask({
      title: `Delete ${l.email}?`,
      body: (
        <>
          <p>
            The lead is removed from every list and sequence. Emails already
            sent stay in the message log.
          </p>
          <p>
            If they asked to have their data erased or never to be contacted,
            also add them to the suppression list, so a later import can't email
            them again.
          </p>
        </>
      ),
      choices: [
        { label: "Cancel", value: "cancel" },
        { label: "Delete", value: "delete", variant: "ghost" },
        {
          label: "Delete and never email",
          value: "suppress",
          variant: "danger-solid",
        },
      ],
    });
    if (!r || r.choice === "cancel") return false;
    await api.DELETE("/v1/brands/{brand}/leads/{id}", { params: { path: { brand: brandId, id: l.id }, query: { suppress: r.choice === "suppress" } } });
    toast(r.choice === "suppress" ? `Deleted ${l.email} and added it to the suppression list.` : `Deleted ${l.email}.`);
    load();
    return true;
  }

  const listName = (id: string) => lists.find((l) => l.id === id)?.name;
  const empty = loaded && total === 0 && !q && !listId && !verification;
  const showImport = canManage && (importing ?? empty);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Leads"
        description="The people your sequences email. Group them into lists, then add a list to a sequence."
        action={
          canManage && (
            <div className="flex gap-2">
              {!empty && (
                <Button
                  variant="ghost"
                  onClick={() => setImporting(!showImport)}
                >
                  {showImport ? "Close import" : "Import leads"}
                </Button>
              )}
              <Button onClick={() => setOpen("new")}>Add lead</Button>
            </div>
          )
        }
      />
      {open && (
        <LeadDialog
          ws={ws}
          lead={open === "new" ? null : open}
          lists={lists}
          canManage={canManage}
          listName={listName}
          onClose={() => setOpen(null)}
          onSaved={() => load()}
          onDelete={async (l) => {
            if (await remove(l)) setOpen(null);
          }}
        />
      )}
      {showImport && <Import ws={ws} onDone={() => load()} go={go} />}
      {!loaded && <Loading />}
      {empty && !canManage && <Empty title="No leads yet" />}
      {loaded && !empty && (
        <div className="space-y-3">
          <DataTable
            data={leads}
            rowId={(l) => l.id}
            onRowClick={(l) => setOpen(l)}
            toolbar={
              <>
                <label className="relative min-w-0 flex-1 basis-60">
                  <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
                  <input
                    type="search"
                    aria-label="Search leads"
                    className={`${field} w-full pl-8`}
                    placeholder="Search email, name or company"
                    value={q}
                    onChange={(e) => setQ(e.target.value)}
                  />
                </label>
                <select
                  aria-label="List"
                  className={field}
                  value={listId}
                  onChange={(e) => setListId(e.target.value)}
                >
                  <option value="">All leads</option>
                  {lists.map((l) => (
                    <option key={l.id} value={l.id}>
                      {l.name} ({l.leads})
                    </option>
                  ))}
                </select>
                <select
                  aria-label="Verification"
                  className={field}
                  value={verification}
                  onChange={(e) => setVerification(e.target.value)}
                >
                  <option value="">Any verification</option>
                  <option value="deliverable">Valid</option>
                  <option value="risky">Risky</option>
                  <option value="undeliverable">Invalid</option>
                  <option value="unknown">Unknown</option>
                  <option value="unverified">Not checked yet</option>
                </select>
                <span className="font-mono text-[12px] text-muted-foreground">
                  {total.toLocaleString()} {total === 1 ? "lead" : "leads"}
                  {listId ? ` in ${listName(listId) ?? "this list"}` : ""}
                </span>
              </>
            }
            empty={
              <>
                No leads match.{" "}
                <button
                  className="underline"
                  onClick={() => {
                    setQ("");
                    setListId("");
                    setVerification("");
                  }}
                >
                  Clear the search
                </button>
              </>
            }
            mobile={(l) => (
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="break-all font-mono text-[13px]">
                    {l.email}
                  </div>
                  <div className="text-xs text-muted-foreground">
                    {[
                      [l.first_name, l.last_name].filter(Boolean).join(" "),
                      l.title,
                      l.company,
                    ]
                      .filter(Boolean)
                      .join(" · ") || "No name"}
                  </div>
                  <div className="mt-1 flex flex-wrap gap-1.5">
                    {l.verification && <Verdict v={l.verification} />}
                    {l.suppressed && (
                      <Badge
                        tone="red"
                        title="On the suppression list: never emailed"
                      >
                        Won't be emailed
                      </Badge>
                    )}
                  </div>
                </div>
              </div>
            )}
            hidden={["created"]}
            columns={[
              {
                id: "email",
                header: "Email",
                accessorFn: (l) => l.email,
                cell: ({ row: { original: l } }) => (
                  <span className="inline-flex flex-wrap items-center gap-1.5">
                    <span className="font-mono text-[12px]">{l.email}</span>
                    {l.suppressed && (
                      <Badge
                        tone="red"
                        title="On the suppression list: never emailed"
                      >
                        Won't be emailed
                      </Badge>
                    )}
                  </span>
                ),
              },
              {
                id: "verification",
                header: "Verified",
                accessorFn: (l) => l.verification ?? "",
                cell: ({ row: { original: l } }) => (
                  <Verdict v={l.verification} />
                ),
              },
              {
                id: "name",
                header: "Name",
                accessorFn: (l) =>
                  [l.first_name, l.last_name].filter(Boolean).join(" "),
                cell: ({ getValue }) =>
                  (getValue() as string) || (
                    <span className="text-muted-foreground">–</span>
                  ),
              },
              {
                id: "company",
                header: "Company",
                accessorFn: (l) => l.company ?? "",
                cell: ({ getValue }) =>
                  (getValue() as string) || (
                    <span className="text-muted-foreground">–</span>
                  ),
              },
              {
                id: "title",
                header: "Title",
                accessorFn: (l) => l.title ?? "",
                cell: ({ getValue }) =>
                  (getValue() as string) || (
                    <span className="text-muted-foreground">–</span>
                  ),
              },
              {
                id: "lists",
                header: "Lists",
                enableSorting: false,
                accessorFn: (l) =>
                  l.list_ids.map(listName).filter(Boolean).join(", "),
                cell: ({ getValue }) => (
                  <span className="text-muted-foreground">
                    {getValue() as string}
                  </span>
                ),
              },
              {
                id: "created",
                header: "Added",
                accessorFn: (l) => l.created_at,
                cell: ({ row: { original: l } }) => (
                  <span className="text-muted-foreground">
                    {ago(l.created_at)}
                  </span>
                ),
              },
              {
                id: "actions",
                header: () => <span className="sr-only">Actions</span>,
                enableSorting: false,
                enableHiding: false,
                meta: { align: "right" },
                cell: ({ row: { original: l } }) =>
                  canManage && (
                    <Button
                      variant="danger"
                      className="-my-1 opacity-0 group-hover:opacity-100 focus-visible:opacity-100"
                      onClick={() => remove(l)}
                    >
                      Delete
                    </Button>
                  ),
              },
            ]}
          />
          {next && (
            <Button variant="ghost" onClick={() => load(next)}>
              Show more ({(total - leads.length).toLocaleString()} left)
            </Button>
          )}
        </div>
      )}
    </div>
  );
}

function Import({ ws, onDone, go }: { ws: string; onDone: () => void; go: (t: Tab) => void }) {
  const brandId = useBrand(ws).brand?.id ?? "";
  const [file, setFile] = useState<File | null>(null);
  const [list, setList] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<LeadImportResult | null>(null);
  const [error, setError] = useState("");

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!file) return;
    setBusy(true);
    const { data, error } = await api.POST("/v1/brands/{brand}/leads/import", {
      params: { path: { brand: brandId }, query: list ? { list } : {} },
      body: await file.text(),
      bodySerializer: (b: string) => b,
      headers: { "Content-Type": "text/csv" },
    });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    setError("");
    setResult(data ?? null);
    setFile(null);
    onDone();
  }

  return (
    <Card
      title="Import leads from a CSV"
      description="Exports from Apollo, Clay, your CRM or a spreadsheet all work."
    >
      <form onSubmit={submit} className="space-y-4 text-sm">
        <p className="text-muted-foreground">
          Only an <code>email</code> column is required. Name, company, title,
          phone, LinkedIn and website columns are recognised by their usual
          names. Every other column is kept as a custom field, so a{" "}
          <code>pain_point</code> column becomes <code>{"{{pain_point}}"}</code>{" "}
          in your emails. Leads already here are updated, not duplicated.
        </p>
        <FilePicker
          file={file}
          onChange={setFile}
          accept=".csv,text/csv"
          example={{
            name: "leads-example.csv",
            content:
              "email,first_name,last_name,company,title,pain_point\nana@acme-com.example,Ana,Diaz,Acme,Founder,slow outbound\n",
          }}
        />
        <div className="flex flex-wrap items-end gap-3">
          <label className="block">
            <span className="mb-1 block font-medium">
              Add them to a list{" "}
              <span className="font-normal text-muted-foreground">
                (optional)
              </span>
            </span>
            <input
              className={field}
              placeholder="e.g. Q4 founders"
              value={list}
              onChange={(e) => setList(e.target.value)}
            />
            <span className="mt-1 block text-xs text-muted-foreground">
              A new name creates the list. You add lists to sequences.
            </span>
          </label>
          <Button
            disabled={!file || busy}
            title={!file ? "Choose a CSV file first" : undefined}
          >
            {busy ? "Importing…" : "Import leads"}
          </Button>
        </div>
        <ErrorText>{error}</ErrorText>
        {result && (
          <div className="rounded bg-muted p-3">
            <p>
              {result.created.toLocaleString()} added,{" "}
              {result.updated.toLocaleString()} updated
              {result.duplicates > 0 &&
                `, ${result.duplicates} duplicate rows merged`}
              {result.errors.length > 0 && `, ${result.errors.length} skipped`}.
              {result.suppressed > 0 &&
                ` ${result.suppressed} are on the suppression list and won't be emailed.`}
            </p>
            {result.list_id && (
              <button
                type="button"
                className="mt-2 underline"
                onClick={() => go("sequences")}
              >
                Add this list to a sequence →
              </button>
            )}
            {result.errors.length > 0 && (
              <ul className="mt-2 max-h-40 space-y-1 overflow-y-auto text-red-600 dark:text-red-400">
                {result.errors.slice(0, 200).map((e) => (
                  <li key={`${e.line}-${e.email}`}>
                    Line {e.line}
                    {e.email ? ` (${e.email})` : ""}: {e.error}
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
      </form>
    </Card>
  );
}

// The lead's own fields, in the order the form shows them.
const leadFields = [
  { key: "first_name", label: "First name" },
  { key: "last_name", label: "Last name" },
  { key: "company", label: "Company" },
  { key: "title", label: "Job title" },
  { key: "phone", label: "Phone" },
  { key: "linkedin_url", label: "LinkedIn URL" },
  { key: "website", label: "Website" },
] as const;
type LeadField = (typeof leadFields)[number]["key"];

// Adds a lead, or shows one to read, change and delete.
function LeadDialog({
  ws,
  lead,
  lists,
  canManage,
  listName,
  onClose,
  onSaved,
  onDelete,
}: {
  ws: string;
  lead: Lead | null;
  lists: LeadList[];
  canManage: boolean;
  listName: (id: string) => string | undefined;
  onClose: () => void;
  onSaved: () => void;
  onDelete: (l: Lead) => void;
}) {
  const brandId = useBrand(ws).brand?.id ?? "";
  const [email, setEmail] = useState(lead?.email ?? "");
  const [companyId, setCompanyId] = useState<string | null>(null);
  const [company, setCompany] = useState<Schemas["Record"] | null>(null);
  const [match, setMatch] = useState<Schemas["Record"] | null>(null);
  const [checking, setChecking] = useState(false);
  const touched = useRef(new Set<string>());
  const { query: brandQuery } = useBrand(ws);
  const [values, setValues] = useState<Record<LeadField, string>>(
    () =>
      Object.fromEntries(
        leadFields.map((f) => [f.key, lead?.[f.key] ?? ""]),
      ) as Record<LeadField, string>,
  );
  const [custom, setCustom] = useState<{ key: string; value: string }[]>(() =>
    Object.entries(lead?.custom ?? {}).map(([key, value]) => ({ key, value })),
  );
  const [listId, setListId] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const readOnly = !canManage;

  function chooseCompany(r: Schemas["Record"]) {
    setCompany(r);
    setCompanyId(r.id);
    setValues((prev) => ({
      ...prev,
      company: displayName(r),
      ...(!touched.current.has("website")
        ? {
            website: String(
              r.attributes.website ??
                (identity(r, "domain")
                  ? `https://${identity(r, "domain")}`
                  : ""),
            ),
          }
        : {}),
    }));
  }
  useEffect(() => {
    if (!lead?.record_id) return;
    let active = true;
    api
      .GET("/v1/workspaces/{ws}/records/{record}", {
        params: { path: { ws, record: lead.record_id } },
      })
      .then(({ data }) => {
        const ids = data?.attributes.companies;
        if (active && Array.isArray(ids) && ids.length)
          setCompanyId(String(ids[0]));
      });
    return () => {
      active = false;
    };
  }, [ws, lead?.record_id]);
  useEffect(() => {
    if (lead || !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email.trim())) {
      setMatch(null);
      return;
    }
    const controller = new AbortController();
    setChecking(true);
    setMatch(null);
    const timer = setTimeout(async () => {
      try {
        const { data } = await api.GET("/v1/workspaces/{ws}/records", {
          params: {
            path: { ws },
            query: {
              type: "person",
              q: email.trim(),
              limit: 20,
              ...brandQuery,
            },
          },
          signal: controller.signal,
        });
        if (controller.signal.aborted) return;
        const matches =
          data?.data.filter((r) =>
            r.identities.some(
              (i) =>
                i.namespace === "email" &&
                i.value.toLowerCase() === email.trim().toLowerCase(),
            ),
          ) ?? [];
        if (matches.length === 1) {
          const r = matches[0];
          setMatch(r);
          const suggestions: Partial<Record<LeadField, string>> = {
            first_name: String(r.attributes.first_name ?? ""),
            last_name: String(r.attributes.last_name ?? ""),
            title: String(r.attributes.job_title ?? ""),
            linkedin_url: String(r.attributes.linkedin_url ?? ""),
            phone: identity(r, "phone") ?? "",
          };
          setValues((prev) => ({
            ...prev,
            ...Object.fromEntries(
              Object.entries(suggestions).filter(
                ([key]) => !touched.current.has(key),
              ),
            ),
          }));
          const refs = r.attributes.companies;
          if (
            Array.isArray(refs) &&
            refs.length === 1 &&
            !touched.current.has("company")
          ) {
            const company = await api.GET(
              "/v1/workspaces/{ws}/records/{record}",
              {
                params: { path: { ws, record: String(refs[0]) } },
                signal: controller.signal,
              },
            );
            if (
              !controller.signal.aborted &&
              company.data &&
              !touched.current.has("company")
            )
              chooseCompany(company.data);
          }
        }
      } catch (e) {
        if (!controller.signal.aborted) setError(errorMessage(e));
      } finally {
        if (!controller.signal.aborted) setChecking(false);
      }
    }, 300);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [ws, email, lead, brandQuery.brand]);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const fields = Object.fromEntries(
      leadFields.map((f) => [f.key, values[f.key].trim()]),
    );
    for (const key of ["website", "linkedin_url"]) {
      if (fields[key] && !/^https?:\/\//i.test(fields[key]))
        fields[key] = `https://${fields[key]}`;
      if (fields[key]) {
        try {
          const url = new URL(fields[key]);
          if (
            !["https:", "http:"].includes(url.protocol) ||
            !url.hostname.includes(".")
          )
            throw new Error();
        } catch {
          setBusy(false);
          return setError(
            `Enter a valid ${key === "website" ? "website" : "LinkedIn URL"}.`,
          );
        }
      }
    }
    if (fields.phone) {
      fields.phone = fields.phone.replace(/[\s().-]/g, "").replace(/^00/, "+");
      if (!/^\+[1-9]\d{6,14}$/.test(fields.phone)) {
        setBusy(false);
        return setError(
          "Include the country code in the phone number, for example +1 202 555 0123.",
        );
      }
    }
    const kept = custom.filter((c) => c.key.trim());
    let res;
    if (lead) {
      // A custom field taken out of the form is removed.
      const removed = Object.keys(lead.custom).filter((k) => !kept.some((c) => c.key.trim() === k));
      res = await api.PATCH("/v1/brands/{brand}/leads/{id}", {
        params: { path: { brand: brandId, id: lead.id } },
        body: { ...fields, ...(companyId ? { company_record_id: companyId } : {}), custom: { ...Object.fromEntries(kept.map((c) => [c.key.trim(), c.value])), ...Object.fromEntries(removed.map((k) => [k, null])) } },
      });
    } else {
      res = await api.POST("/v1/brands/{brand}/leads", {
        params: { path: { brand: brandId } },
        body: {
          email: email.trim().toLowerCase(),
          ...(companyId ? { company_record_id: companyId } : {}),
          ...Object.fromEntries(Object.entries(fields).filter(([, v]) => v)),
          ...(kept.length
            ? {
                custom: Object.fromEntries(
                  kept.map((c) => [c.key.trim(), c.value]),
                ),
              }
            : {}),
          ...(listId ? { list_id: listId } : {}),
        },
      });
    }
    setBusy(false);
    if (res.error) return setError(errorMessage(res.error));
    toast(lead ? `Saved ${lead.email}.` : `Added ${email.trim()}.`);
    onSaved();
    onClose();
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <form className="space-y-3 text-sm" onSubmit={submit}>
          <DialogHeader>
            <DialogTitle className="break-all">
              {lead ? lead.email : "Add a lead"}
            </DialogTitle>
            <DialogDescription>
              {lead ? (
                <>
                  Added {ago(lead.created_at)}
                  {lead.source ? ` from ${lead.source}` : ""}. The email address
                  can't be changed: add the new address as a lead instead.
                </>
              ) : (
                "Start with an email. Existing contact details appear automatically; choose a company to link its record."
              )}
            </DialogDescription>
          </DialogHeader>
          {lead && (
            <div className="flex flex-wrap items-center gap-1.5">
              <Verdict v={lead.verification} />
              {lead.suppressed && (
                <Badge
                  tone="red"
                  title="On the suppression list: never emailed"
                >
                  Won't be emailed
                </Badge>
              )}
              {lead.list_ids
                .map((id) => listName(id))
                .filter(Boolean)
                .map((n) => (
                  <Badge key={n} tone="grey">
                    {n}
                  </Badge>
                ))}
            </div>
          )}
          {!lead && (
            <label className="block">
              <span className="mb-1 block font-medium">Email</span>
              <input
                type="email"
                required
                autoFocus
                className={`${field} w-full`}
                placeholder="ana@acme.example"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
              />
            </label>
          )}
          {checking && (
            <p
              role="status"
              className="flex items-center gap-2 text-xs text-muted-foreground"
            >
              <Loader2 className="size-3 animate-spin" />
              Checking existing contacts…
            </p>
          )}
          {match && (
            <p className="flex items-center gap-2 rounded-lg border border-emerald-500/20 bg-emerald-500/5 p-3 text-xs">
              <CheckCircle2 className="size-4 text-emerald-600" />
              {displayName(match)} is already in your CRM. Known details are
              filled in.
            </p>
          )}
          <div className="rounded-lg border bg-muted/20 p-3">
            <label htmlFor="lead-company" className="mb-2 block font-medium">
              Company
            </label>
            {readOnly ? (
              <p>{values.company || "No company"}</p>
            ) : (
              <RecordPicker
                ws={ws}
                id="lead-company"
                type="account"
                value={companyId}
                names={
                  companyId ? { [companyId]: values.company || "Company" } : {}
                }
                canCreate
                onChange={(v) => {
                  touched.current.add("company");
                  setCompanyId(v as string | null);
                  if (!v) {
                    setCompany(null);
                    setValues((prev) => ({ ...prev, company: "" }));
                  }
                }}
                onSelect={chooseCompany}
              />
            )}
            {company && (
              <div className="mt-2 flex items-center gap-2 text-xs text-muted-foreground">
                <Building2 className="size-3.5" />
                {[
                  identity(company, "domain"),
                  company.attributes.industry,
                  company.attributes.company_size,
                ]
                  .filter(Boolean)
                  .map(String)
                  .join(" · ")}
              </div>
            )}
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            {leadFields
              .filter((f) => f.key !== "company")
              .map((f) => (
                <label
                  key={f.key}
                  className={
                    f.key === "linkedin_url" || f.key === "website"
                      ? "block sm:col-span-2"
                      : "block"
                  }
                >
                  <span className="mb-1 block font-medium">{f.label}</span>
                  <input
                    type={f.key === "phone" ? "tel" : "text"}
                    autoComplete={
                      f.key === "phone"
                        ? "tel"
                        : f.key === "first_name"
                          ? "given-name"
                          : f.key === "last_name"
                            ? "family-name"
                            : "off"
                    }
                    placeholder={
                      f.key === "phone"
                        ? "+1 202 555 0123"
                        : f.key === "website"
                          ? "company.example"
                          : undefined
                    }
                    className={`${field} w-full`}
                    readOnly={readOnly}
                    value={values[f.key]}
                    onChange={(e) => {
                      touched.current.add(f.key);
                      setValues({ ...values, [f.key]: e.target.value });
                    }}
                  />
                </label>
              ))}
          </div>
          <details className="rounded-lg border p-3">
            <summary className="cursor-pointer font-medium">
              Additional email personalization
            </summary>
            <div className="space-y-2">
              {custom.map((c, i) => (
                <div key={i} className="flex gap-2">
                  <input
                    aria-label="Field name"
                    className={`${field} w-2/5`}
                    placeholder="pain_point"
                    readOnly={readOnly}
                    value={c.key}
                    onChange={(e) =>
                      setCustom(
                        custom.map((x, j) =>
                          j === i ? { ...x, key: e.target.value } : x,
                        ),
                      )
                    }
                  />
                  <input
                    aria-label="Value"
                    className={`${field} min-w-0 flex-1`}
                    readOnly={readOnly}
                    value={c.value}
                    onChange={(e) =>
                      setCustom(
                        custom.map((x, j) =>
                          j === i ? { ...x, value: e.target.value } : x,
                        ),
                      )
                    }
                  />
                  {canManage && (
                    <Button
                      type="button"
                      variant="ghost"
                      onClick={() =>
                        setCustom(custom.filter((_, j) => j !== i))
                      }
                    >
                      Remove
                    </Button>
                  )}
                </div>
              ))}
              {custom.length === 0 && readOnly && (
                <p className="text-muted-foreground">None.</p>
              )}
              {canManage && (
                <button
                  type="button"
                  className="underline"
                  onClick={() => setCustom([...custom, { key: "", value: "" }])}
                >
                  Add a custom field
                </button>
              )}
            </div>
          </details>
          {!lead && lists.length > 0 && (
            <label className="block">
              <span className="mb-1 block font-medium">
                Add to a list{" "}
                <span className="font-normal text-muted-foreground">
                  (optional)
                </span>
              </span>
              <select
                className={`${field} w-full`}
                value={listId}
                onChange={(e) => setListId(e.target.value)}
              >
                <option value="">No list</option>
                {lists.map((l) => (
                  <option key={l.id} value={l.id}>
                    {l.name}
                  </option>
                ))}
              </select>
            </label>
          )}
          {lead?.record_id && (
            <div className="text-[13px]">
              <RecordPartners ws={ws} record={lead.record_id} />
            </div>
          )}
          <ErrorText>{error}</ErrorText>
          <DialogFooter className="flex-row justify-end gap-2">
            {lead && canManage && (
              <Button
                type="button"
                variant="danger"
                className="mr-auto"
                onClick={() => onDelete(lead)}
              >
                Delete
              </Button>
            )}
            <Button type="button" variant="ghost" onClick={onClose}>
              {canManage ? "Cancel" : "Close"}
            </Button>
            {canManage && (
              <Button disabled={busy}>
                {busy ? "Saving…" : lead ? "Save" : "Add lead"}
              </Button>
            )}
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
