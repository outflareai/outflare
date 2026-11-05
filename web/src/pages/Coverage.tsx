import { useEffect, useState } from "react";
import { CoverageExtraction, CoverageOutcomes } from "./brand-coverage/enrichment";
import { PlacementParties } from "./brand-coverage/parties";
import { PresenceMomentum } from "./brand-coverage/momentum";
import { WebSearches } from "./brand-coverage/web-searches";
import { CompetitorPresence } from "./brand-coverage/competitors";
import { Opportunities } from "./brand-coverage/opportunities";
import {
  FilterBuilder,
  emptyFilters,
  type FilterGroup,
} from "./brand-coverage/filters";
import {
  ArrowUpRight,
  CheckCircle2,
  CircleHelp,
  Globe2,
  Plus,
  RefreshCw,
  Search,
  ShieldCheck,
  TrendingUp,
} from "lucide-react";
import { api, errorMessage } from "../api";
import { useBrand } from "../lib/brand";
import { track } from "../lib/analytics";
import { appPath, type Tab } from "../lib/paths";
import {
  Badge,
  Button,
  CopyButton,
  Empty,
  ErrorText,
  field,
  Loading,
  PageHeader,
  toast,
} from "../ui";
import { DataTable } from "../components/data-table";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "../components/ui/sheet";
import { Tabs, TabsList, TabsTrigger } from "../components/ui/tabs";
import {
  csv,
  label,
  list,
  nameOf,
  parseCSV,
  resources,
  save,
  type Definition,
  type Field,
  type Resource,
  type Row,
} from "./brand-coverage/client";

type Props = {
  ws: string;
  myId: string;
  can: (scope: string) => boolean;
  go: (tab: Tab, item?: string) => void;
};
type Section =
  | "overview"
  | "tasks"
  | "placements"
  | "publishers"
  | "commercial"
  | "candidates"
  | "competitors"
  | "opportunities"
  | "reports"
  | "rules"
  | "settings"
  | "delivery_reports";
const sections: [Section, string][] = [
  ["overview", "Overview"],
  ["tasks", "Work queue"],
  ["placements", "Placements"],
  ["publishers", "Publishers"],
  ["commercial", "Commercial"],
  ["candidates", "Discovery"],
  ["opportunities", "Opportunities"],
  ["competitors", "Competitors"],
  ["reports", "Reports"],
];
const titles: Partial<Record<Resource, string>> = {
  tasks: "Work queue",
  candidates: "Discovery",
  rules: "Maintenance automations",
  finance_tasks: "Commercial work",
  delivery_reports: "Reported delivery",
};
const hints: Partial<Record<Resource, string>> = {
  placements:
    "Every mention, backlink and paid placement. Track the relationship from first contact through ongoing maintenance.",
  publishers:
    "Connect a website to its CRM company. Add another website for publishers with several domains.",
  tasks:
    "Evidence, ownership and follow-up in one place. Snoozing a task does not pause monitoring.",
  candidates:
    "Review discoveries before adding them to your coverage. Finding a link does not make it an earned placement.",
  agreements:
    "One agreement can fund many placements. Its commitment is counted once.",
  rules:
    "Fixed maintenance triggers create owned work when evidence or deadlines change. Disable a trigger to stop future tasks; existing tasks remain in your work queue.",
  delivery_reports:
    "Record dated delivery evidence from a publisher or analytics tool. Unknown counts stay blank. Reported delivery is separate from page checks and verified attribution.",
  documents:
    "Link contracts, media kits, creative and proof of delivery to an agreement. Access to the source document is controlled by its host.",
  finance_tasks:
    "Invoice, renewal, notice and delivery reminders. Review the source record before taking action.",
  payments:
    "Record settlements and explicit reversals. These records do not send money.",
};
const color = (s: unknown) =>
  ["up", "live", "active", "resolved", "accepted", "confirmed", "present"].includes(
    String(s),
  )
    ? "green"
    : ["gone", "urgent", "disputed", "removed"].includes(String(s))
      ? "red"
      : ["unknown", "unchecked", "archived", "dismissed"].includes(String(s))
        ? "grey"
        : "blue";
const display = (v: unknown) =>
  v == null || v === ""
    ? "—"
    : typeof v === "boolean"
      ? v
        ? "Yes"
        : "No"
      : typeof v === "object"
        ? JSON.stringify(v)
        : String(v);
const money = (n: unknown, currency: unknown) => {
  try {
    return new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: String(currency),
    }).format(
      Number(n) /
        10 **
          (new Intl.NumberFormat(undefined, {
            style: "currency",
            currency: String(currency),
          }).resolvedOptions().maximumFractionDigits ?? 2),
    );
  } catch {
    return `${n} minor units ${currency}`;
  }
};

export function Coverage(props: Props) {
  const { brand, loaded } = useBrand(props.ws);
  if (!loaded) return <Loading />;
  if (!brand)
    return (
      <Empty title="Choose a brand for Presence">
        Select a brand in the sidebar to manage its publisher relationships,
        placements and commercial activity.
      </Empty>
    );
  if (!props.can("coverage.read"))
    return (
      <Empty title="Presence access needed">
        Ask a workspace admin for Coverage read access.
      </Empty>
    );
  return <CoverageBrand key={brand.id} {...props} brand={brand.id} />;
}
function CoverageBrand({
  ws,
  brand,
  can,
  go,
  myId,
}: Props & { brand: string }) {
  const initial = new URLSearchParams(location.search);
  const [section, setSection] = useState<Section>(
    [
      ...sections.map(([s]) => s),
      "rules",
      "settings",
      "delivery_reports",
    ].includes(initial.get("view") as Section)
      ? (initial.get("view") as Section)
      : "overview",
  );
  const [commercial, setCommercial] = useState<Resource>("agreements");
  const [placementView, setPlacementView] = useState<{
    basic: Record<string, string>;
    advanced: FilterGroup;
  }>({ basic: {}, advanced: emptyFilters });
  const [defs, setDefs] = useState<Definition[]>([]);
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);
  const [editing, setEditing] = useState<{ resource: Resource; row?: Row }>();
  const [selected, setSelected] = useState<{ resource: Resource; row: Row }>();
  const [importing, setImporting] = useState(false);
  const [expanded, setExpanded] = useState(initial.get("expanded") === "1");
  const resource: Resource =
    section === "commercial"
      ? commercial
      : section === "overview" ||
          section === "reports" ||
          section === "settings" ||
          section === "competitors" ||
          section === "opportunities"
        ? "placements"
        : section;
  const def = defs.find((d) => d.name === resource);
  const manage = can(
    def?.finance ? "coverage.finance.manage" : "coverage.manage",
  );
  useEffect(() => {
    let active = true;
    api
      .GET("/v1/brands/{brand}/coverage/schema", {
        params: { path: { brand } },
      })
      .then(({ data, error }) => {
        if (!active) return;
        if (error || !data) setError(errorMessage(error));
        else setDefs(data.data as unknown as Definition[]);
      })
      .catch((e) => active && setError(String(e)));
    return () => {
      active = false;
    };
  }, [brand]);
  const navigate = (s: Section) => {
    setSection(s);
    const u = new URL(location.href);
    u.searchParams.set("view", s);
    u.searchParams.delete("record");
    history.replaceState(null, "", u);
    track("ui.coverage_view_opened", { view: s });
  };
  const open = (resource: Resource, row: Row) => {
    setSelected({ resource, row });
    const u = new URL(location.href);
    u.searchParams.set("record", `${resource}/${row.id}`);
    history.replaceState(null, "", u);
  };
  useEffect(() => {
    const record = initial.get("record")?.split("/");
    if (!record || !resources.includes(record[0] as Resource)) return;
    const r = record[0] as Resource;
    api
      .GET(
        `/v1/brands/{brand}/coverage/${r}/{id}` as "/v1/brands/{brand}/coverage/placements/{id}",
        { params: { path: { brand, id: record[1] } } },
      )
      .then(({ data, error }) => {
        if (error) setError(errorMessage(error));
        else if (data)
          setSelected({ resource: r, row: data as unknown as Row });
      });
  }, [brand]);
  const close = () => {
    setSelected(undefined);
    const u = new URL(location.href);
    u.searchParams.delete("record");
    history.replaceState(null, "", u);
  };
  const refresh = () => {
    setReload((n) => n + 1);
    if (selected)
      void api
        .GET(
          `/v1/brands/{brand}/coverage/${selected.resource}/{id}` as "/v1/brands/{brand}/coverage/placements/{id}",
          { params: { path: { brand, id: selected.row.id } } },
        )
        .then(({ data, error }) => {
          if (error) setError(errorMessage(error));
          else if (data)
            setSelected((current) =>
              current?.row.id === selected.row.id
                ? { ...current, row: data as unknown as Row }
                : current,
            );
        })
        .catch((e) => setError(String(e)));
  };
  return (
    <div className="space-y-6">
      <PageHeader
        title={
          <span className="flex items-center gap-2">
            <Globe2 className="size-5" />
            Presence
          </span>
        }
        description="Everywhere your brand appears: placements, mentions, links, videos, posts, reviews and listings. Keep each one accurate and every relationship connected."
        action={
          <div className="flex gap-2">
            <Button variant="ghost" onClick={() => navigate("rules")}>
              Automations
            </Button>
            <Button variant="ghost" onClick={() => navigate("settings")}>
              Settings
            </Button>
            {can("coverage.manage") && (
              <Button onClick={() => setEditing({ resource: "placements" })}>
                <Plus className="size-4" />
                Add placement
              </Button>
            )}
          </div>
        }
      />
      <Tabs value={section} onValueChange={(v) => navigate(v as Section)}>
        <TabsList className="flex h-auto max-w-full justify-start overflow-x-auto">
          {sections
            .filter(([s]) => s !== "commercial" || can("coverage.finance.read"))
            .map(([s, title]) => (
              <TabsTrigger className="shrink-0" key={s} value={s}>
                {title}
              </TabsTrigger>
            ))}
        </TabsList>
      </Tabs>
      <ErrorText>{error}</ErrorText>
      {section === "settings" ? (
        <CoverageSettings go={go} can={can} />
      ) : section === "opportunities" ? (
        <Opportunities brand={brand} manage={can("coverage.manage")} myId={myId} />
      ) : section === "competitors" ? (
        <CompetitorPresence ws={ws} brand={brand} manage={can("coverage.manage")} />
      ) : section === "overview" || section === "reports" ? (
        <CoverageOverview
          brand={brand}
          detailed={section === "reports"}
          definition={defs.find((d) => d.name === "placements")}
          manage={can("coverage.manage")}
          reload={reload}
          finance={can("coverage.finance.read")}
          analytics={can("analytics.read")}
          onWork={() => navigate("tasks")}
          onPlacements={(basic, advanced) => {
            setPlacementView({ basic, advanced });
            navigate("placements");
          }}
        />
      ) : (
        <>
          {section === "rules" && (
            <CoverageRules
              brand={brand}
              defs={defs}
              canManage={manage}
              refresh={refresh}
            />
          )}
          {section === "commercial" && (
            <div className="flex flex-wrap gap-2">
              {defs
                .filter((d) => d.finance)
                .map((d) => (
                  <Button
                    key={d.name}
                    variant={commercial === d.name ? "primary" : "ghost"}
                    onClick={() => setCommercial(d.name)}
                  >
                    {label(d.name)}
                  </Button>
                ))}
            </div>
          )}
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div>
              <h2 className="text-lg font-semibold">
                {titles[resource] ?? label(resource)}
              </h2>
              <p className="mt-1 max-w-3xl text-sm text-muted-foreground">
                {hints[resource] ??
                  "Manage the terms, delivery and financial records behind your placements."}
              </p>
            </div>
            <div className="flex gap-2">
              {resource === "placements" && (
                <Button
                  variant="ghost"
                  onClick={() => navigate("delivery_reports")}
                >
                  Reported delivery
                </Button>
              )}
              {resource === "placements" && manage && (
                <Button variant="ghost" onClick={() => setImporting(true)}>
                  Import CSV
                </Button>
              )}
              {manage && (
                <Button
                  variant="ghost"
                  onClick={() => setEditing({ resource })}
                >
                  <Plus className="size-3.5" />
                  Add{" "}
                  {resource === "tasks"
                    ? "task"
                    : resource === "candidates"
                      ? "candidate"
                      : resource.replace(/s$/, "")}
                </Button>
              )}
            </div>
          </div>
          {section === "candidates" && (
            <WebSearches ws={ws} brand={brand} manage={manage} />
          )}
          {section === "candidates" && can("seo.read") && (
            <CoverageSearches
              ws={ws}
              brand={brand}
              canManage={can("seo.manage")}
            />
          )}
          {section === "candidates" && (
            <div className="flex flex-wrap items-center gap-3 rounded-lg border bg-muted/20 p-4 text-sm">
              <Search className="size-4" />
              <span className="flex-1">
                New finds come in by themselves every day from news searches,
                backlinks, link prospects, social watches, AI answers that name
                the brand, and web searches. Refresh to bring them in now.
              </span>
              <Button variant="ghost" onClick={() => go("news")}>
                Searches
              </Button>
              <Button variant="ghost" onClick={() => go("backlinks")}>
                Backlink sources
              </Button>
              <Button
                disabled={!manage}
                onClick={async () => {
                  try {
                    const { data, error } = await api.POST(
                      "/v1/brands/{brand}/coverage/discovery/reconcile",
                      { params: { path: { brand } } },
                    );
                    if (error) throw new Error(errorMessage(error));
                    if (data && "added" in data) {
                      toast(
                        `Added ${data.added} discoveries${data.has_more ? "; refresh again to import the next batch" : ""}`,
                      );
                    } else
                      toast(
                        "Discovery refresh is waiting for mission approval",
                      );
                    refresh();
                  } catch (e) {
                    setError(String(e));
                  }
                }}
              >
                Refresh discoveries
              </Button>
            </div>
          )}
          {def && (
            <CoverageList
              myId={myId}
              ws={ws}
              manage={manage}
              canSaveViews={can("coverage.manage")}
              key={resource}
              brand={brand}
              definition={def}
              initialView={
                resource === "placements" ? placementView : undefined
              }
              reload={reload}
              open={(row) => open(resource, row)}
            />
          )}
        </>
      )}
      <Sheet open={!!editing} onOpenChange={(v) => !v && setEditing(undefined)}>
        <SheetContent className="w-full overflow-y-auto sm:max-w-2xl">
          <SheetHeader>
            <SheetTitle>
              {editing?.row ? "Edit" : "Add"}{" "}
              {editing?.resource.replace(/s$/, "")}
            </SheetTitle>
            <SheetDescription>
              Changes are saved to this brand. CRM contacts and conversations
              stay linked to their original records.
            </SheetDescription>
          </SheetHeader>
          {editing && defs.find((d) => d.name === editing.resource) && (
            <CoverageForm
              key={editing.row?.id ?? editing.resource}
              ws={ws}
              brand={brand}
              definition={defs.find((d) => d.name === editing.resource)!}
              can={can}
              row={editing.row}
              onSaved={(row) => {
                setEditing(undefined);
                refresh();
                if (selected?.row.id === row.id)
                  setSelected({ ...selected, row });
              }}
            />
          )}
        </SheetContent>
      </Sheet>
      <Sheet open={!!selected && !editing} onOpenChange={(v) => !v && close()}>
        <SheetContent
          className={`w-full overflow-y-auto ${expanded ? "sm:max-w-none" : "sm:max-w-3xl"}`}
        >
          <div className="mb-3 flex gap-2 pr-8">
            <Button
              variant="ghost"
              onClick={() => {
                const next = !expanded;
                setExpanded(next);
                const url = new URL(location.href);
                if (next) url.searchParams.set("expanded", "1");
                else url.searchParams.delete("expanded");
                history.replaceState(null, "", url);
                track("ui.coverage_view_opened", {
                  view: "record",
                  expanded: next,
                });
              }}
            >
              {expanded ? "Use side panel" : "Expand detail"}
            </Button>
            <CopyButton label="Copy record link" text={location.href} />
          </div>
          {selected && (
            <CoverageDetail
              key={`${selected.resource}/${selected.row.id}`}
              {...selected}
              ws={ws}
              brand={brand}
              can={can}
              go={go}
              definition={defs.find((d) => d.name === selected.resource)}
              edit={() => setEditing(selected)}
              review={(resource, row) => setEditing({ resource, row })}
              refresh={refresh}
              open={open}
            />
          )}
        </SheetContent>
      </Sheet>
      <Sheet open={importing} onOpenChange={setImporting}>
        <SheetContent className="w-full overflow-y-auto sm:max-w-2xl">
          <SheetHeader>
            <SheetTitle>Import placements</SheetTitle>
            <SheetDescription>
              Preview up to 200 rows. Duplicate page and inventory combinations
              are skipped; each invalid row shows its own result.
            </SheetDescription>
          </SheetHeader>
          <CoverageImport
            ws={ws}
            definition={defs.find((d) => d.name === "placements")}
            canCreate={can("customers.manage")}
            brand={brand}
            done={() => {
              refresh();
            }}
          />
        </SheetContent>
      </Sheet>
    </div>
  );
}
function CoverageList({
  ws,
  myId,
  manage,
  canSaveViews,
  brand,
  definition: def,
  reload,
  initialView,
  open,
}: {
  ws: string;
  myId: string;
  manage: boolean;
  canSaveViews: boolean;
  brand: string;
  definition: Definition;
  reload: number;
  initialView?: { basic: Record<string, string>; advanced: FilterGroup };
  open: (row: Row) => void;
}) {
  const [rows, setRows] = useState<Row[]>([]),
    [total, setTotal] = useState(0),
    // The cursor of each page shown so far, the current one last.
    [pages, setPages] = useState<string[]>([""]),
    [next, setNext] = useState<string | null>(null),
    [q, setQ] = useState("");
  const [filters, setFilters] = useState<Record<string, string>>(
      initialView?.basic ?? {},
    ),
    [loading, setLoading] = useState(true),
    [error, setError] = useState("");
  const [pipeline, setPipeline] = useState(false);
  const offset = (pages.length - 1) * 50;
  const [bulkError, setBulkError] = useState("");
  const [advanced, setAdvanced] = useState<FilterGroup>(
    initialView?.advanced ?? emptyFilters,
  );
  const [views, setViews] = useState<Row[]>([]),
    [viewName, setViewName] = useState(""),
    [viewVisibility, setViewVisibility] = useState("private");
  const [selected, setSelected] = useState<string[]>([]),
    [bulkOwner, setBulkOwner] = useState(""),
    [bulkStatus, setBulkStatus] = useState(""),
    [bulkResolution, setBulkResolution] = useState(""),
    [bulkBusy, setBulkBusy] = useState(false),
    [localReload, setLocalReload] = useState(0);
  const viewSection = [
    "placements",
    "publishers",
    "tasks",
    "agreements",
    "candidates",
  ].includes(def.name)
    ? def.name
    : "agreements";
  const readViews = () =>
    list(brand, "views", {
      limit: 200,
      filters: JSON.stringify({
        mode: "all",
        rules: [{ field: "section", op: "eq", value: viewSection }],
      }),
    })
      .then((r) =>
        setViews(
          r.data.filter((v) => {
            try {
              return JSON.parse(String(v.filters)).resource === def.name;
            } catch {
              return false;
            }
          }),
        ),
      )
      .catch((e) => setError(String(e)));
  useEffect(() => {
    void readViews();
  }, [brand, def.name]);
  const queryFilters = JSON.stringify({
    ...advanced,
    rules: advanced.rules.filter((r) => r.op === "empty" || r.value !== ""),
  });
  const bulk = async (body: Record<string, unknown>) => {
    setBulkError("");
    setBulkBusy(true);
    let changed = 0;
    const failed: string[] = [];
    for (const row of rows.filter((r) => selected.includes(r.id))) {
      try {
        await save(brand, def.name, body, row);
        changed++;
      } catch (e) {
        failed.push(`${nameOf(row)}: ${String(e)}`);
      }
    }
    setBulkBusy(false);
    setSelected([]);
    setLocalReload((n) => n + 1);
    toast(`Updated ${changed} records`);
    if (failed.length) setBulkError(failed.join("; "));
  };

  useEffect(() => {
    let active = true;
    setLoading(true);
    const timer = setTimeout(() => {
      list(brand, def.name, {
        ...filters,
        q,
        filters: queryFilters,
        limit: 50,
        ...(pages[pages.length - 1] ? { cursor: pages[pages.length - 1] } : {}),
      })
        .then((r) => {
          if (active) {
            setRows(r.data);
            setTotal(r.total);
            setNext(r.next_cursor);
            setError("");
          }
        })
        .catch((e) => active && setError(String(e)))
        .finally(() => active && setLoading(false));
    }, 180);
    return () => {
      active = false;
      clearTimeout(timer);
    };
  }, [brand, def.name, q, filters, queryFilters, pages, reload, localReload]);
  const columns =
    def.name === "placements"
      ? [
          "title",
          "kind",
          "arrangement",
          "stage",
          "health",
          "link_status",
          "mention_status",
          "traction",
          "published_on",
        ]
      : def.name === "publishers"
        ? ["domain", "category", "status", "notes"]
        : def.name === "tasks"
          ? ["title", "reason", "priority", "status", "due_on", "owner_id"]
          : def.fields
              .filter((f) => !f.reference && f.kind !== "long")
              .map((f) => f.name)
              .slice(0, 7);
  const exportRows = async () => {
    try {
      const all: Row[] = [];
      for (let cursor = ""; ; ) {
        const r = await list(brand, def.name, {
          ...filters,
          filters: queryFilters,
          q,
          limit: 200,
          ...(cursor ? { cursor } : {}),
        });
        all.push(...r.data);
        if (!r.next_cursor || !r.data.length) break;
        cursor = r.next_cursor;
      }
      const blob = new Blob(
        [csv(all, ["id", ...def.fields.map((f) => f.name)])],
        { type: "text/csv;charset=utf-8" },
      );
      const u = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = u;
      a.download = `coverage-${def.name}.csv`;
      a.click();
      URL.revokeObjectURL(u);
      track("ui.coverage_exported", { resource: def.name, count: all.length });
    } catch (e) {
      setError(String(e));
    }
  };
  return (
    <div className="space-y-4">
      {["tasks", "finance_tasks", "placements"].includes(def.name) && (
        <div className="flex flex-wrap gap-2">
          {(def.name === "placements"
            ? ["All placements", "Earned", "Paid", "Lost", "Needs verification"]
            : [
                "All work",
                "My work",
                "Unassigned",
                "Due",
                "Snoozed",
                "Resolved",
              ]
          ).map((title) => (
            <Button
              key={title}
              variant="ghost"
              onClick={() => {
                const rule = (field: string, op: string, value = "") => ({
                  field,
                  op,
                  value,
                });
                let rules: FilterGroup["rules"] = [];
                if (title === "My work")
                  rules = [
                    rule("owner_id", "eq", myId),
                    rule("status", "ne", "resolved"),
                    rule("status", "ne", "dismissed"),
                  ];
                if (title === "Unassigned")
                  rules = [
                    rule("owner_id", "empty"),
                    rule("status", "ne", "resolved"),
                    rule("status", "ne", "dismissed"),
                  ];
                if (title === "Due") {
                  const tomorrow = new Date();
                  tomorrow.setDate(tomorrow.getDate() + 1);
                  rules = [
                    rule(
                      "due_on",
                      "before",
                      tomorrow.toISOString().slice(0, 10),
                    ),
                    rule("status", "ne", "resolved"),
                    rule("status", "ne", "dismissed"),
                    rule("status", "ne", "snoozed"),
                  ];
                }
                if (title === "Snoozed" || title === "Resolved")
                  rules = [rule("status", "eq", title.toLowerCase())];
                if (title === "Earned" || title === "Paid")
                  rules = [rule("arrangement", "eq", title.toLowerCase())];
                if (title === "Lost") rules = [rule("health", "eq", "gone")];
                if (title === "Needs verification")
                  rules = [rule("health", "eq", "unchecked")];
                setAdvanced({ mode: "all", rules });
                setQ("");
                setFilters({});
                setPages([""]);
                track("ui.coverage_filters_changed", {
                  resource: def.name,
                  count: rules.length,
                });
              }}
            >
              {title}
            </Button>
          ))}
        </div>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <select
          aria-label="Saved coverage view"
          className={`${field} !w-auto`}
          defaultValue=""
          onChange={(e) => {
            const view = views.find((v) => v.id === e.target.value);
            if (!view) return;
            try {
              const data = JSON.parse(String(view.filters));
              setFilters(data.filters ?? {});
              setQ(data.q ?? "");
              setAdvanced(data.advanced ?? emptyFilters);
              setPages([""]);
              setPipeline(!!data.pipeline);
            } catch {
              setError("This saved view could not be loaded.");
            }
          }}
        >
          <option value="">Saved views</option>
          {views.map((v) => (
            <option key={v.id} value={v.id}>
              {nameOf(v)} · {display(v.visibility)}
            </option>
          ))}
        </select>
        {canSaveViews && (
          <>
            <input
              aria-label="New view name"
              className={`${field} max-w-48`}
              placeholder="Name this view"
              value={viewName}
              onChange={(e) => setViewName(e.target.value)}
            />
            <select
              aria-label="View visibility"
              className={`${field} !w-auto`}
              value={viewVisibility}
              onChange={(e) => setViewVisibility(e.target.value)}
            >
              <option value="private">Only me</option>
              <option value="team">Team</option>
            </select>
            <Button
              variant="ghost"
              disabled={!viewName.trim()}
              onClick={async () => {
                try {
                  await save(brand, "views", {
                    title: viewName,
                    section: viewSection,
                    visibility: viewVisibility,
                    filters: JSON.stringify({
                      q,
                      filters,
                      advanced,
                      pipeline,
                      resource: def.name,
                    }),
                  });
                  setViewName("");
                  await readViews();
                  toast("View saved");
                } catch (e) {
                  setError(String(e));
                }
              }}
            >
              Save view
            </Button>
          </>
        )}
      </div>
      <ErrorText>{bulkError}</ErrorText>
      <FilterBuilder
        definition={def}
        value={advanced}
        onChange={(v) => {
          setAdvanced(v);
          setPages([""]);
          track("ui.coverage_filters_changed", {
            resource: def.name,
            count: v.rules.length,
          });
        }}
      />
      {manage && def.name !== "payments" && (
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <label className="flex items-center gap-2">
            <input
              type="checkbox"
              checked={rows.length > 0 && selected.length === rows.length}
              onChange={(e) =>
                setSelected(e.target.checked ? rows.map((r) => r.id) : [])
              }
            />
            Select this page
          </label>
          <span className="text-muted-foreground">
            {selected.length} selected
          </span>
          {selected.length > 0 && (
            <>
              <Button variant="ghost" onClick={() => setSelected([])}>
                Clear
              </Button>
              {def.fields.some((f) => f.name === "owner_id") && (
                <div className="flex items-start gap-2">
                  <ReferencePicker
                    ws={ws}
                    brand={brand}
                    field={def.fields.find((f) => f.name === "owner_id")!}
                    value={bulkOwner}
                    onChange={setBulkOwner}
                  />
                  <Button
                    disabled={bulkBusy}
                    onClick={() => bulk({ owner_id: bulkOwner || null })}
                  >
                    {bulkOwner ? "Assign" : "Unassign"}
                  </Button>
                </div>
              )}
              {def.fields.some(
                (f) => f.name === "status" || f.name === "stage",
              ) && (
                <>
                  <select
                    aria-label="Bulk status"
                    className={`${field} !w-auto`}
                    value={bulkStatus}
                    onChange={(e) => setBulkStatus(e.target.value)}
                  >
                    <option value="">Choose status</option>
                    {def.fields
                      .find((f) => f.name === "status" || f.name === "stage")
                      ?.choices.map((v) => (
                        <option key={v} value={v}>
                          {label(v)}
                        </option>
                      ))}
                  </select>
                  {["tasks", "finance_tasks"].includes(def.name) &&
                    ["resolved", "dismissed"].includes(bulkStatus) && (
                      <input
                        aria-label="Bulk resolution"
                        className={`${field} max-w-xs`}
                        value={bulkResolution}
                        placeholder="Resolution or dismissal reason"
                        onChange={(e) => setBulkResolution(e.target.value)}
                      />
                    )}
                  <Button
                    disabled={bulkBusy || !bulkStatus}
                    onClick={() =>
                      bulk({
                        [def.fields.some((f) => f.name === "stage")
                          ? "stage"
                          : "status"]: bulkStatus,
                        ...(["tasks", "finance_tasks"].includes(def.name) &&
                        ["resolved", "dismissed"].includes(bulkStatus)
                          ? { resolution: bulkResolution }
                          : {}),
                      })
                    }
                  >
                    Apply status
                  </Button>
                </>
              )}
            </>
          )}
        </div>
      )}
      <div className="flex flex-wrap gap-2">
        <label className="relative min-w-48 flex-1">
          <Search className="absolute left-3 top-2.5 size-4 text-muted-foreground" />
          <input
            aria-label="Search coverage"
            className={`${field} pl-9`}
            placeholder={`Search ${def.name}…`}
            value={q}
            onChange={(e) => {
              setQ(e.target.value);
              setPages([""]);
            }}
          />
        </label>
        {def.fields
          .filter((f) =>
            ["status", "stage", "kind", "arrangement"].includes(f.name),
          )
          .map((f) => (
            <select
              key={f.name}
              aria-label={label(f.name)}
              className={`${field} !w-auto`}
              value={filters[f.name] ?? ""}
              onChange={(e) => {
                setFilters({ ...filters, [f.name]: e.target.value });
                setPages([""]);
              }}
            >
              <option value="">All {label(f.name).toLowerCase()}</option>
              {f.choices.map((c) => (
                <option key={c} value={c}>
                  {label(c)}
                </option>
              ))}
            </select>
          ))}
        <Button variant="ghost" onClick={exportRows}>
          Export
        </Button>
        {def.name === "placements" && (
          <Button variant="ghost" onClick={() => setPipeline(!pipeline)}>
            {pipeline ? "Table" : "Pipeline"}
          </Button>
        )}
      </div>
      <ErrorText>{error}</ErrorText>
      {loading ? (
        <Loading />
      ) : pipeline ? (
        <div className="flex gap-3 overflow-x-auto pb-3">
          {def.fields
            .find((f) => f.name === "stage")
            ?.choices.map((stage) => (
              <div
                key={stage}
                className="w-60 shrink-0 rounded-lg border bg-muted/15 p-3"
              >
                <h3 className="mb-3 text-xs font-semibold uppercase tracking-wide">
                  {label(stage)} ·{" "}
                  {rows.filter((r) => r.stage === stage).length}
                </h3>
                {rows
                  .filter((r) => r.stage === stage)
                  .map((r) => (
                    <button
                      key={r.id}
                      className="mb-2 w-full rounded-md border bg-background p-3 text-left hover:border-brand"
                      onClick={() => open(r)}
                    >
                      <div className="text-sm font-medium">{nameOf(r)}</div>
                      <div className="mt-2 text-xs text-muted-foreground">
                        {label(String(r.kind))} · {label(String(r.arrangement))}
                      </div>
                    </button>
                  ))}
              </div>
            ))}
        </div>
      ) : (
        <DataTable
          data={rows}
          rowId={(r) => r.id}
          onRowClick={open}
          columns={[
            ...(manage && def.name !== "payments"
              ? [
                  {
                    id: "select",
                    header: "",
                    enableSorting: false,
                    cell: ({ row }: { row: { original: Row } }) => (
                      <input
                        type="checkbox"
                        aria-label={`Select ${nameOf(row.original)}`}
                        checked={selected.includes(row.original.id)}
                        onClick={(e) => e.stopPropagation()}
                        onChange={(e) =>
                          setSelected((old) =>
                            e.target.checked
                              ? [...old, row.original.id]
                              : old.filter((id) => id !== row.original.id),
                          )
                        }
                      />
                    ),
                  },
                ]
              : []),
            ...columns.map((k) => ({
              accessorKey: k,
              header:
                (
                  {
                    kind: "Format",
                    arrangement: "Acquisition",
                    expected_on: "Expected live",
                    published_on: "Published",
                  } as Record<string, string>
                )[k] ?? label(k.replace(/_id$/, "")),
              cell: ({
                row,
              }: {
                row: { original: Record<string, unknown> };
              }) =>
                [
                  "status",
                  "stage",
                  "health",
                  "arrangement",
                  "priority",
                ].includes(k) ? (
                  <Badge tone={color(row.original[k])}>
                    {label(display(row.original[k]))}
                  </Badge>
                ) : k === "amount_minor" ? (
                  money(row.original[k], row.original.currency)
                ) : (
                  <span
                    className={
                      k === "title" || k === "domain"
                        ? "font-medium"
                        : "text-muted-foreground"
                    }
                  >
                    {k === "kind"
                      ? label(display(row.original[k]))
                      : display(row.original[k])}
                  </span>
                ),
            })),
          ]}
          mobile={(r) => (
            <div className="space-y-2">
              <div className="flex items-start gap-2">
                {manage && def.name !== "payments" && (
                  <input
                    className="mt-1"
                    type="checkbox"
                    aria-label={`Select ${nameOf(r)}`}
                    checked={selected.includes(r.id)}
                    onClick={(e) => e.stopPropagation()}
                    onChange={(e) =>
                      setSelected((old) =>
                        e.target.checked
                          ? [...old, r.id]
                          : old.filter((id) => id !== r.id),
                      )
                    }
                  />
                )}
                <p className="font-medium">{nameOf(r)}</p>
              </div>
              <p className="text-xs text-muted-foreground">
                {label(display(r.stage ?? r.status))} ·{" "}
                {label(display(r.kind ?? r.reason))}
              </p>
            </div>
          )}
          empty={
            <Empty title={`No ${def.name} yet`}>
              {q || Object.values(filters).some(Boolean)
                ? "Try clearing a filter."
                : "Add your first record to start building the picture."}
            </Empty>
          }
        />
      )}
      <div className="flex items-center justify-between text-xs text-muted-foreground">
        <span>
          {total ? offset + 1 : 0}–{Math.min(offset + rows.length, total)} of {total}
          {pipeline && " · Pipeline shows this page of results"}
        </span>
        <div className="flex gap-2">
          <Button
            variant="ghost"
            disabled={pages.length < 2}
            onClick={() => setPages(pages.slice(0, -1))}
          >
            Previous
          </Button>
          <Button
            variant="ghost"
            disabled={!next}
            onClick={() => next && setPages([...pages, next])}
          >
            Next
          </Button>
        </div>
      </div>
    </div>
  );
}
function CoverageForm({
  ws,
  brand,
  can,
  definition: def,
  row,
  onSaved,
}: {
  ws: string;
  brand: string;
  can: Props["can"];
  definition: Definition;
  row?: Row;
  onSaved: (row: Row) => void;
}) {
  const currentBrand = useBrand(ws).byId(brand);
  const [values, setValues] = useState<Record<string, unknown>>(() =>
    Object.fromEntries(
      def.fields.map((f) => {
        const v =
          row?.[f.name] ??
          (!row && def.name === "placements" && f.name === "expected_mention"
            ? currentBrand?.name
            : undefined) ??
          f.default ??
          "";
        return [
          f.name,
          f.kind === "money" && def.name !== "delivery_reports" && v !== ""
            ? String(
                Number(v) /
                  10 ** currencyDigits(String(row?.currency ?? "USD")),
              )
            : v,
        ];
      }),
    ),
  );
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const set = (key: string, value: unknown) =>
    setValues((v) => ({ ...v, [key]: value }));
  useEffect(() => {
    if (def.name !== "deliverables" || !values.agreement_id) return;
    let active = true;
    setValues((v) => ({ ...v, __currency: undefined }));
    api
      .GET("/v1/brands/{brand}/coverage/agreements/{id}", {
        params: { path: { brand, id: String(values.agreement_id) } },
      })
      .then(({ data, error }) => {
        if (!active) return;
        if (error) setError(errorMessage(error));
        else if (data) {
          setValues((v) => ({
            ...v,
            __currency: data.currency,
            ...(row && row.agreement_id === v.agreement_id
              ? {
                  allocation_minor: String(
                    Number(row.allocation_minor) /
                      10 ** currencyDigits(data.currency),
                  ),
                }
              : {}),
          }));
        }
      })
      .catch((e) => active && setError(String(e)));
    return () => {
      active = false;
    };
  }, [brand, def.name, values.agreement_id]);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const body: Record<string, unknown> = {};
      for (const f of def.fields) {
        const v = values[f.name];
        if (v === "" && f.nullable) body[f.name] = null;
        else if (f.kind === "money" && def.name !== "delivery_reports")
          body[f.name] = parseMoney(
            String(v),
            String(values.currency ?? values.__currency ?? "USD"),
          );
        else if (["money", "number", "hours", "count"].includes(f.kind))
          body[f.name] = Number(v);
        else body[f.name] = v;
      }
      onSaved(await save(brand, def.name, body, row));
      toast("Saved");
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <form onSubmit={submit} className="space-y-5 p-5">
      <ErrorText>{error}</ErrorText>
      {formGroups(def).map(([title, fields], index) => (
        <details
          key={title}
          open={index === 0 || !!row}
          className="rounded-lg border p-4"
        >
          <summary className="cursor-pointer font-medium">{title}</summary>
          <div className="mt-4 space-y-5">
            {fields.map((f) => (
              <div key={f.name} className="space-y-1.5">
                <label
                  className="block text-sm font-medium"
                  htmlFor={`coverage-${f.name}`}
                >
                  {label(f.name.replace(/_id$/, ""))}
                  {f.required && (
                    <span className="text-muted-foreground"> *</span>
                  )}
                </label>
                {f.reference ? (
                  <ReferencePicker
                    ws={ws}
                    brand={brand}
                    field={f}
                    allowCreate={
                      can("customers.manage") && can("coverage.manage")
                    }
                    value={String(values[f.name] ?? "")}
                    onChange={(v) => set(f.name, v)}
                  />
                ) : f.kind === "enum" ? (
                  <select
                    id={`coverage-${f.name}`}
                    className={field}
                    value={String(values[f.name])}
                    onChange={(e) => set(f.name, e.target.value)}
                  >
                    {f.choices.map((c) => (
                      <option key={c} value={c}>
                        {label(c)}
                      </option>
                    ))}
                  </select>
                ) : f.kind === "bool" ? (
                  <label className="flex items-center gap-2 text-sm">
                    <input
                      id={`coverage-${f.name}`}
                      type="checkbox"
                      checked={!!values[f.name]}
                      onChange={(e) => set(f.name, e.target.checked)}
                    />
                    {f.name === "monitor"
                      ? "Check this placement on its schedule"
                      : f.name === "monitor_accuracy"
                        ? "Review changed content with workspace AI (at most daily; usage budget applies)"
                        : f.name === "baseline"
                          ? "Existing coverage imported as a historical baseline"
                          : f.name === "enabled"
                            ? "Enable this maintenance rule"
                            : "Required by this placement"}
                  </label>
                ) : f.kind === "long" ? (
                  <textarea
                    id={`coverage-${f.name}`}
                    rows={4}
                    className={field}
                    value={String(values[f.name])}
                    onChange={(e) => set(f.name, e.target.value)}
                  />
                ) : (
                  <input
                    id={`coverage-${f.name}`}
                    className={field}
                    required={f.required}
                    type={
                      f.kind === "date"
                        ? "date"
                        : ["money", "number", "hours", "count"].includes(f.kind)
                          ? "number"
                          : f.kind === "url"
                            ? "url"
                            : "text"
                    }
                    min={f.kind === "hours" ? 1 : 0}
                    step={
                      f.kind === "number" ||
                      (f.kind === "money" && def.name !== "delivery_reports")
                        ? "any"
                        : 1
                    }
                    value={String(values[f.name])}
                    onChange={(e) => set(f.name, e.target.value)}
                  />
                )}
                {f.kind === "money" && def.name !== "delivery_reports" && (
                  <p className="text-xs text-muted-foreground">
                    Enter the amount in{" "}
                    {String(
                      values.currency ??
                        values.__currency ??
                        "the agreement currency",
                    )}
                    , including decimals where used.
                  </p>
                )}
                {f.name === "inventory" && (
                  <p className="text-xs text-muted-foreground">
                    Ad slot, newsletter edition or other location. Different
                    inventory can share a page URL.
                  </p>
                )}
                {f.name === "expected_mention" && (
                  <p className="text-xs text-muted-foreground">
                    Exact brand or product text to look for in the page sample.
                  </p>
                )}
              </div>
            ))}
          </div>
        </details>
      ))}
      <div className="sticky bottom-0 border-t bg-background py-3">
        <Button
          type="submit"
          disabled={busy || (def.name === "deliverables" && !values.__currency)}
        >
          {busy ? "Saving…" : "Save"}
        </Button>
      </div>
    </form>
  );
}
function ReferencePicker({
  ws,
  brand,
  field: f,
  value,
  onChange,
  allowCreate = false,
}: {
  ws: string;
  brand: string;
  field: Field;
  value: string;
  onChange: (v: string) => void;
  allowCreate?: boolean;
}) {
  const [q, setQ] = useState(""),
    [options, setOptions] = useState<{ id: string; name: string }[]>([]),
    [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    const load = async () => {
      try {
        let options: { id: string; name: string }[] = [];
        if (resources.includes(f.reference as Resource))
          options = (
            await list(brand, f.reference as Resource, { q, limit: 100 })
          ).data.map((r) => ({ id: r.id, name: nameOf(r) }));
        else if (f.reference === "records") {
          const responses = [
            await api.GET("/v1/brands/{brand}/records", {
              params: {
                path: { brand },
                query: {
                  type: f.name === "record_id" ? "account" : "person",
                  q,
                  limit: 50,
                },
              },
            }),
          ];
          const found = new Map<string, { id: string; name: string }>();
          for (const result of responses) {
            if (result.error) throw new Error(errorMessage(result.error));
            for (const r of result.data?.data ?? [])
              found.set(r.id, { id: r.id, name: r.name || r.id });
          }
          options = [...found.values()];
        } else if (f.reference === "users") {
          const { data, error } = await api.GET("/v1/workspaces/{ws}/members", {
            params: { path: { ws } },
          });
          if (error) throw new Error(errorMessage(error));
          options = (data?.data ?? [])
            .filter(
              (r) =>
                ["owner", "admin"].includes(r.role ?? "") ||
                !r.brand_ids?.length ||
                r.brand_ids.includes(brand),
            )
            .map((r) => ({
              id: r.user_id ?? "",
              name: r.name || r.user_id || "Member",
            }));
        } else if (f.reference === "deals") {
          const { data, error } = await api.GET("/v1/workspaces/{ws}/deals", {
            params: { path: { ws }, query: { brand } },
          });
          if (error) throw new Error(errorMessage(error));
          options = (data?.data ?? []).map((r) => ({
            id: r.id,
            name: r.name || r.id,
          }));
        }
        if (active) {
          setOptions(options);
          setError("");
        }
      } catch (e) {
        if (active) setError(String(e));
      }
    };
    const timer = setTimeout(load, 200);
    return () => {
      active = false;
      clearTimeout(timer);
    };
  }, [ws, brand, f.reference, f.name, q]);
  return (
    <div className="space-y-2">
      <input
        className={field}
        aria-label={`Search ${f.reference}`}
        placeholder={`Search ${label(f.reference).toLowerCase()}…`}
        value={q}
        onChange={(e) => setQ(e.target.value)}
      />
      <select
        id={`coverage-${f.name}`}
        className={field}
        value={value}
        required={f.required}
        onChange={(e) => onChange(e.target.value)}
      >
        <option value="">{f.required ? "Choose a record" : "None"}</option>
        {value && !options.some((o) => o.id === value) && (
          <option value={value}>Linked record · {value.slice(0, 8)}</option>
        )}
        {options.map((o) => (
          <option key={o.id} value={o.id}>
            {o.name}
          </option>
        ))}
      </select>
      <ErrorText>{error}</ErrorText>
      {allowCreate && ["records", "publishers"].includes(f.reference) && (
        <CoverageCreateReference
          ws={ws}
          brand={brand}
          field={f}
          onCreated={(r) => {
            setOptions((v) => [...v, { id: r.id, name: nameOf(r) }]);
            onChange(r.id);
          }}
        />
      )}
    </div>
  );
}
function CoverageDetail({
  ws,
  brand,
  resource,
  row,
  definition: def,
  can,
  go,
  edit,
  review,
  refresh,
  open,
}: {
  ws: string;
  brand: string;
  resource: Resource;
  row: Row;
  definition?: Definition;
  can: Props["can"];
  go: Props["go"];
  edit: () => void;
  review: (resource: Resource, row: Row) => void;
  refresh: () => void;
  open: (resource: Resource, row: Row) => void;
}) {
  const [purpose, setPurpose] = useState<
    "classification" | "thank_you" | "link_request" | "correction" | "follow_up"
  >("thank_you");
  const [tab, setTab] = useState("overview"),
    [historyRows, setHistoryRows] = useState<Row[]>([]),
    [observations, setObservations] = useState<Row[]>([]),
    [related, setRelated] = useState<Row[]>([]),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [publisher, setPublisher] = useState("");
  useEffect(() => {
    let active = true;
    setError("");
    api
      .GET("/v1/brands/{brand}/coverage/{resource}/{id}/history", {
        params: { path: { brand, resource, id: row.id } },
      })
      .then(({ data, error }) => {
        if (!active) return;
        if (error) setError(errorMessage(error));
        else setHistoryRows((data?.data ?? []) as Row[]);
      });
    if (resource === "placements")
      api
        .GET("/v1/brands/{brand}/coverage/placements/{id}/observations", {
          params: { path: { brand, id: row.id } },
        })
        .then(({ data, error }) => {
          if (!active) return;
          if (error) setError(errorMessage(error));
          else setObservations((data?.data ?? []) as Row[]);
        });
    if (resource === "publishers" || resource === "agreements")
      list(brand, "placements", {
        [resource === "publishers" ? "publisher_id" : "agreement_id"]: row.id,
      })
        .then((r) => active && setRelated(r.data))
        .catch((e) => active && setError(String(e)));
    return () => {
      active = false;
    };
  }, [brand, resource, row.id, row.version]);
  useEffect(() => {
    if (resource !== "placements" || tab !== "checks") return;
    let active = true;
    const timer = setInterval(async () => {
      try {
        const result = await api.GET(
          "/v1/brands/{brand}/coverage/placements/{id}/observations",
          { params: { path: { brand, id: row.id } } },
        );
        if (active && result.data) setObservations(result.data.data as Row[]);
      } catch (e) {
        if (active) setError(String(e));
      }
    }, 5000);
    return () => {
      active = false;
      clearInterval(timer);
    };
  }, [brand, resource, row.id, tab]);
  const action = async (
    fn: () => Promise<{ error?: unknown }>,
    message: string,
  ) => {
    setBusy(true);
    try {
      const r = await fn();
      if (r.error) throw new Error(errorMessage(r.error));
      toast(message);
      refresh();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };
  const crmID = row.record_id ?? row.contact_record_id;
  return (
    <>
      <SheetHeader>
        <SheetTitle className="pr-6 leading-normal">{nameOf(row)}</SheetTitle>
        <SheetDescription>
          {label(resource)} ·{" "}
          {row.url ? String(row.url) : "Relationship and placement details"}
        </SheetDescription>
      </SheetHeader>
      <div className="space-y-5 p-5">
        <div className="flex flex-wrap gap-2">
          {row.stage != null && (
            <Badge tone={color(row.stage)}>{label(String(row.stage))}</Badge>
          )}
          {row.health != null && (
            <Badge tone={color(row.health)}>{label(String(row.health))}</Badge>
          )}
          {row.arrangement != null && (
            <Badge tone="grey">{label(String(row.arrangement))}</Badge>
          )}
          {resource === "placements" && row.link_status != null && row.link_status !== "unknown" && (
            <Badge tone={color(row.link_status)}>
              {row.link_status === "removed" ? "Link removed" : row.link_status === "present" ? "Links to us" : "No link"}
            </Badge>
          )}
          {resource === "placements" && row.mention_status != null && row.mention_status !== "unknown" && (
            <Badge tone={color(row.mention_status)}>
              {row.mention_status === "removed" ? "Mention removed" : row.mention_status === "present" ? "Mentions us" : "No mention"}
            </Badge>
          )}
          {resource === "placements" && (row.traction === "taking_off" || row.traction === "quiet") && (
            <Badge
              tone={row.traction === "taking_off" ? "green" : "grey"}
              title={`Cited by ${Number(row.ai_citations_90d ?? 0)} AI answers in 90 days`}
            >
              {row.traction === "taking_off" ? "Taking off" : "Quiet"}
            </Badge>
          )}
          {resource === "placements" && row.offline_since ? (
            <Badge tone="red">Offline since {new Date(String(row.offline_since)).toLocaleDateString()}</Badge>
          ) : null}
          {can(def?.finance ? "coverage.finance.manage" : "coverage.manage") &&
            resource !== "payments" && (
              <Button variant="ghost" onClick={edit}>
                Edit
              </Button>
            )}
          {row.url ? (
            <a
              className="inline-flex items-center gap-1 text-sm text-brand"
              href={String(row.url)}
              target="_blank"
              rel="noopener noreferrer"
            >
              Visit page
              <ArrowUpRight className="size-3" />
            </a>
          ) : null}
        </div>
        <ErrorText>{error}</ErrorText>
        <Tabs value={tab} onValueChange={setTab}>
          <TabsList>
            <TabsTrigger value="overview">Overview</TabsTrigger>
            <TabsTrigger value="relationship">Relationship</TabsTrigger>
            {resource === "placements" && (
              <TabsTrigger value="checks">Content & links</TabsTrigger>
            )}
            <TabsTrigger value="history">History</TabsTrigger>
          </TabsList>
        </Tabs>
        {tab === "overview" && (
          <>
            {resource === "documents" && (
              <CoverageExtraction
                brand={brand}
                document={row}
                manage={can("coverage.finance.manage")}
                review={review}
              />
            )}
            <dl className="grid gap-4 sm:grid-cols-2">
              {def?.fields
                .filter((f) => !f.reference && f.kind !== "long")
                .map((f) => (
                  <div key={f.name}>
                    <dt className="text-xs text-muted-foreground">
                      {label(f.name)}
                    </dt>
                    <dd className="mt-1 break-words text-sm">
                      {f.kind === "money" && row.currency
                        ? money(row[f.name], row.currency)
                        : display(row[f.name])}
                    </dd>
                  </div>
                ))}
            </dl>
            {def?.fields
              .filter((f) => f.kind === "long" && row[f.name])
              .map((f) => (
                <div key={f.name}>
                  <h3 className="text-sm font-medium">{label(f.name)}</h3>
                  <p className="mt-2 whitespace-pre-wrap text-sm text-muted-foreground">
                    {String(row[f.name])}
                  </p>
                </div>
              ))}
            {resource === "placements" && (
              <div className="rounded-lg border p-4 space-y-2">
                <h3 className="font-medium">Reported delivery</h3>
                <p className="text-sm text-muted-foreground">
                  Record impressions, clicks or conversions from a dated source.
                  These are kept separate from page availability.
                </p>
                <CoverageRelated
                  brand={brand}
                  resource="delivery_reports"
                  filter={{ placement_id: row.id }}
                  open={open}
                />
              </div>
            )}
            {resource === "agreements" && (
              <div className="rounded-lg border p-4">
                <p className="mb-3 text-sm">
                  {row.approved_version === row.version
                    ? `Terms approved for version ${row.version}.`
                    : "Current terms have not been approved. Editing terms clears approval."}
                </p>
                {can("coverage.approve") && (
                  <Button
                    disabled={busy || row.approved_version === row.version}
                    onClick={() =>
                      action(
                        () =>
                          api.POST(
                            "/v1/brands/{brand}/coverage/agreements/{id}/approve",
                            {
                              params: { path: { brand, id: row.id } },
                              body: { version: row.version },
                            },
                          ),
                        "Terms approved",
                      )
                    }
                  >
                    Approve current terms
                  </Button>
                )}
              </div>
            )}
            {resource === "agreements" && can("coverage.finance.read") && (
              <div className="space-y-5">
                {(
                  [
                    "deliverables",
                    "invoices",
                    "documents",
                    "finance_tasks",
                  ] as Resource[]
                ).map((r) => (
                  <section key={r}>
                    <h3 className="mb-2 font-medium">
                      {titles[r] ?? label(r)}
                    </h3>
                    <CoverageRelated
                      brand={brand}
                      resource={r}
                      filter={{ agreement_id: row.id }}
                      open={open}
                    />
                  </section>
                ))}
              </div>
            )}
            {related.length > 0 && (
              <div>
                <h3 className="mb-2 font-medium">Placements</h3>
                {related.map((r) => (
                  <button
                    className="flex w-full items-center justify-between border-b py-3 text-left text-sm"
                    key={r.id}
                    onClick={() => open("placements", r)}
                  >
                    {nameOf(r)}
                    <Badge tone={color(r.health)}>{display(r.health)}</Badge>
                  </button>
                ))}
              </div>
            )}
            {resource === "candidates" && can("coverage.manage") && (
              <div className="space-y-3 rounded-lg border p-4">
                <h3 className="font-medium">Add to placements</h3>
                <ReferencePicker
                  ws={ws}
                  brand={brand}
                  field={{
                    name: "publisher_id",
                    kind: "uuid",
                    reference: "publishers",
                    required: true,
                    nullable: false,
                    choices: [],
                    default: null,
                  }}
                  value={publisher}
                  onChange={setPublisher}
                />
                <Button
                  disabled={busy || !publisher}
                  onClick={() =>
                    action(async () => {
                      const r = await api.POST(
                        "/v1/brands/{brand}/coverage/candidates/{id}/confirm",
                        {
                          params: { path: { brand, id: row.id } },
                          body: { publisher_id: publisher },
                        },
                      );
                      if (r.data && "id" in r.data)
                        open("placements", r.data as unknown as Row);
                      return r;
                    }, "Discovery confirmed")
                  }
                >
                  Confirm placement
                </Button>
              </div>
            )}
          </>
        )}
        {tab === "relationship" && (
          <div className="space-y-4">
            <CoverageRelationshipContext
              ws={ws}
              brand={brand}
              record={crmID ? String(crmID) : undefined}
              publisherID={
                row.publisher_id ? String(row.publisher_id) : undefined
              }
              allowed={can("customers.read")}
            />
            {resource === "placements" && (
              <PlacementParties
                ws={ws}
                brand={brand}
                placement={row}
                manage={can("coverage.manage")}
                readCRM={can("customers.read")}
              />
            )}
            <div className="rounded-lg border p-4">
              <h3 className="font-medium">Keep the relationship in context</h3>
              <p className="mt-2 text-sm text-muted-foreground">
                Review prior conversations, relationship ownership and contact
                preferences before reaching out. A thank-you and a backlink
                request are separate actions.
              </p>
              <div className="mt-4 flex flex-wrap gap-2">
                {crmID ? (
                  <a
                    className="text-sm text-brand underline"
                    href={appPath(ws, "people", String(crmID))}
                  >
                    Open linked CRM record
                  </a>
                ) : (
                  <Button variant="ghost" onClick={() => go("people")}>
                    Find a contact in CRM
                  </Button>
                )}
                <Button variant="ghost" onClick={() => go("inbox")}>
                  Conversations
                </Button>
                <Button variant="ghost" onClick={() => go("sequences")}>
                  Approved outreach
                </Button>
              </div>
            </div>
            {row.publisher_id ? (
              <Button
                variant="ghost"
                onClick={async () => {
                  const { data, error } = await api.GET(
                    "/v1/brands/{brand}/coverage/publishers/{id}",
                    {
                      params: { path: { brand, id: String(row.publisher_id) } },
                    },
                  );
                  if (error) setError(errorMessage(error));
                  else if (data) open("publishers", data as unknown as Row);
                }}
              >
                View publisher
              </Button>
            ) : null}
          </div>
        )}
        {tab === "checks" && (
          <div className="space-y-4">
            <div className="flex items-center justify-between gap-3">
              <p className="text-sm text-muted-foreground">
                Observations are time-stamped evidence. An unavailable check
                does not prove a lost placement.
              </p>
              {can("coverage.manage") && (
                <Button
                  disabled={busy || !row.url}
                  onClick={() =>
                    action(
                      () =>
                        api.POST(
                          "/v1/brands/{brand}/coverage/placements/{id}/check",
                          { params: { path: { brand, id: row.id } } },
                        ),
                      "Check queued. Results appear after the worker finishes.",
                    )
                  }
                >
                  <RefreshCw className="size-3.5" />
                  Check now
                </Button>
              )}
              {can("coverage.manage") && (
                <Button
                  variant="ghost"
                  disabled={busy || !row.url}
                  onClick={() =>
                    action(
                      () =>
                        api.POST(
                          "/v1/brands/{brand}/coverage/placements/{id}/check",
                          {
                            params: {
                              path: { brand, id: row.id },
                              query: { reader: "connected" },
                            },
                          },
                        ),
                      "Check queued through your connected page reader and its usage budget",
                    )
                  }
                >
                  Check with Geonode
                </Button>
              )}
              {can("coverage.manage") && can("workspace.read") && (
                <Button
                  variant="ghost"
                  disabled={
                    busy || !observations.some((o) => o.health === "up")
                  }
                  onClick={() =>
                    action(
                      () =>
                        api.POST(
                          "/v1/brands/{brand}/coverage/placements/{id}/review",
                          { params: { path: { brand, id: row.id } } },
                        ),
                      "Accuracy review queued using your workspace AI model",
                    )
                  }
                >
                  Review accuracy
                </Button>
              )}
            </div>
            {can("coverage.manage") && can("workspace.read") && (
              <div className="flex flex-wrap items-center gap-2 rounded-lg border bg-muted/20 p-3">
                <select
                  aria-label="AI assistance purpose"
                  className={`${field} !w-auto`}
                  value={purpose}
                  onChange={(e) => setPurpose(e.target.value as typeof purpose)}
                >
                  {[
                    "thank_you",
                    "link_request",
                    "correction",
                    "follow_up",
                    "classification",
                  ].map((p) => (
                    <option value={p} key={p}>
                      {label(p)}
                    </option>
                  ))}
                </select>
                <Button
                  variant="ghost"
                  disabled={
                    busy || !observations.some((o) => o.health === "up")
                  }
                  onClick={() =>
                    action(
                      () =>
                        api.POST(
                          "/v1/brands/{brand}/coverage/placements/{id}/review",
                          {
                            params: {
                              path: { brand, id: row.id },
                              query: { purpose },
                            },
                          },
                        ),
                      "AI assistance queued. Review its evidence and draft before using it.",
                    )
                  }
                >
                  Ask AI
                </Button>
                <p className="w-full text-xs text-muted-foreground">
                  Uses your workspace model and budget. Drafts stay here for
                  review. Check Relationship for prior conversations, ownership
                  and contact preferences before using approved outreach.
                </p>
              </div>
            )}
            {observations.length === 0 ? (
              <Empty title="No observations yet">
                Add a page URL and run a check, or wait for the next scheduled
                check.
              </Empty>
            ) : (
              observations.map((o) => {
                const e = o.evidence as Record<string, unknown>;
                const assessment = e.assessment as
                  | {
                      sentiment: string;
                      summary: string;
                      draft_subject?: string;
                      draft_body?: string;
                      suggested_kind?: string;
                      findings: {
                        item_id: string;
                        version: number;
                        quote: string;
                        knowledge_quote?: string;
                        verdict: string;
                        explanation: string;
                        stale?: boolean;
                      }[];
                    }
                  | undefined;
                return (
                  <div key={o.id} className="space-y-3 rounded-lg border p-4">
                    <div className="flex items-center justify-between">
                      <Badge tone={color(o.health)}>{display(o.health)}</Badge>
                      <time className="text-xs text-muted-foreground">
                        {new Date(o.created_at).toLocaleString()}
                      </time>
                    </div>
                    {o.source === "ai_review" && (
                      <div className="space-y-3">
                        <h3 className="text-sm font-medium">
                          {label(String(e.purpose ?? "accuracy"))} ·{" "}
                          {label(String(e.status))}
                        </h3>
                        {assessment ? (
                          <>
                            <p className="text-sm">{assessment.summary}</p>
                            <p className="text-xs text-muted-foreground">
                              Sentiment: {label(assessment.sentiment)} · AI
                              findings need human review.
                            </p>
                            {assessment.draft_body && (
                              <CoverageDraft
                                subject={assessment.draft_subject ?? ""}
                                body={assessment.draft_body}
                              />
                            )}
                            {assessment.suggested_kind && (
                              <div className="flex items-center gap-2 text-sm">
                                Suggested format:{" "}
                                {label(assessment.suggested_kind)}
                                {can("coverage.manage") && (
                                  <Button
                                    variant="ghost"
                                    onClick={() =>
                                      action(async () => {
                                        await save(
                                          brand,
                                          "placements",
                                          { kind: assessment.suggested_kind },
                                          row,
                                        );
                                        return {};
                                      }, "Suggested format applied")
                                    }
                                  >
                                    Apply format
                                  </Button>
                                )}
                              </div>
                            )}
                            {assessment.findings.map((f, i) => (
                              <div
                                key={i}
                                className="space-y-2 rounded border p-3 text-sm"
                              >
                                <Badge tone={f.stale ? "grey" : "blue"}>
                                  {f.stale
                                    ? "Source changed — recheck"
                                    : label(f.verdict)}
                                </Badge>
                                <blockquote className="border-l-2 pl-3">
                                  {f.quote}
                                </blockquote>
                                <p className="text-muted-foreground">
                                  {f.explanation}
                                </p>
                                {f.knowledge_quote && (
                                  <div className="rounded bg-muted/40 p-3">
                                    <p className="mb-1 text-xs text-muted-foreground">
                                      Published Knowledge · version {f.version}
                                    </p>
                                    <blockquote>{f.knowledge_quote}</blockquote>
                                  </div>
                                )}
                                <a
                                  href={appPath(ws, "knowledge", f.item_id)}
                                  className="text-xs text-brand underline"
                                >
                                  Knowledge source · version {f.version}
                                </a>
                              </div>
                            ))}
                          </>
                        ) : (
                          <p className="text-sm text-muted-foreground">
                            {e.status === "knowledge_needed"
                              ? "Publish customer-facing facts in Knowledge before reviewing this coverage."
                              : e.status === "model_unavailable"
                                ? "Connect an AI model and check its budget under Connections and AI settings."
                                : "The review could not establish a current assessment. Check its sources and try again."}
                          </p>
                        )}
                      </div>
                    )}
                    {e.error_code ? (
                      <p className="text-sm text-amber-500">
                        {e.error_code === "connector_needed"
                          ? "Connect your own Geonode account to use the connected page reader."
                          : `Check needs attention: ${label(String(e.error_code))}`}
                      </p>
                    ) : null}
                    <p className="text-sm">{display(e.title)}</p>
                    <p className="text-xs text-muted-foreground">
                      HTTP {display(e.http_status)} · Mention in text sample:{" "}
                      {display(e.mention_present)}
                    </p>
                    {Array.isArray(e.links) &&
                      e.links.map((l: Record<string, unknown>, i: number) => (
                        <div
                          key={i}
                          className="rounded bg-muted/30 p-2 text-xs"
                        >
                          <a
                            href={String(l.url)}
                            target="_blank"
                            rel="noopener noreferrer"
                            className="break-all text-brand"
                          >
                            {String(l.url)}
                          </a>
                          <p className="mt-1">
                            Anchor: {display(l.anchor)} · Rel:{" "}
                            {Array.isArray(l.rel) && l.rel.length
                              ? l.rel.join(", ")
                              : "No restrictive rel observed"}
                          </p>
                        </div>
                      ))}
                    <details>
                      <summary className="cursor-pointer text-xs text-muted-foreground">
                        Page text sample
                      </summary>
                      <p className="mt-2 whitespace-pre-wrap text-xs">
                        {display(e.text)}
                      </p>
                    </details>
                  </div>
                );
              })
            )}
          </div>
        )}
        {tab === "history" && (
          <div className="space-y-3">
            {historyRows.length === 0 ? (
              <Empty title="No activity yet" />
            ) : (
              historyRows.map((h) => (
                <div className="border-l-2 pl-4" key={h.id}>
                  <div className="text-sm font-medium">
                    {label(String(h.action))}
                  </div>
                  <time className="text-xs text-muted-foreground">
                    {new Date(h.created_at).toLocaleString()}
                  </time>
                </div>
              ))
            )}
          </div>
        )}
      </div>
    </>
  );
}
function CoverageOverview({
  brand,
  definition,
  manage,
  detailed,
  reload,
  finance,
  analytics,
  onWork,
  onPlacements,
}: {
  brand: string;
  definition?: Definition;
  manage: boolean;
  detailed: boolean;
  reload: number;
  finance: boolean;
  analytics: boolean;
  onWork: () => void;
  onPlacements: (basic: Record<string, string>, advanced: FilterGroup) => void;
}) {
  const [data, setData] = useState<Record<string, unknown>>(),
    [costs, setCosts] = useState<Record<string, unknown>[]>([]),
    [error, setError] = useState("");
  const [reportQuery, setReportQuery] = useState<{
      from?: string;
      to?: string;
      filters?: string;
    }>({}),
    [frozen, setFrozen] = useState<Record<string, unknown>>();
  useEffect(() => {
    if (frozen) {
      setData(frozen);
      setCosts([]);
      setError("");
      return;
    }
    let active = true;
    setError("");
    api
      .GET("/v1/brands/{brand}/coverage/reports", {
        params: { path: { brand }, query: reportQuery },
      })
      .then(({ data, error }) => {
        if (!active) return;
        if (error) setError(errorMessage(error));
        else setData(data);
      });
    if (finance)
      api
        .GET("/v1/brands/{brand}/coverage/finance-report", {
          params: { path: { brand } },
        })
        .then(({ data, error }) => {
          if (!active) return;
          if (error) setError(errorMessage(error));
          else setCosts(data?.data ?? []);
        });
    return () => {
      active = false;
    };
  }, [brand, reload, finance, JSON.stringify(reportQuery), frozen]);

  if (!data) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  const growth = (data.growth ?? []) as {
    month: string;
    discovered: number;
    published: number;
    first_verified: number;
  }[];
  const maximum = Math.max(
    1,
    ...growth.flatMap((g) => [g.discovered, g.published, g.first_verified]),
  );
  return (
    <div className="space-y-6">
      <CoverageReportTools
        brand={brand}
        manage={manage}
        definition={definition}
        query={reportQuery}
        frozen={!!frozen}
        apply={(q) => {
          setFrozen(undefined);
          setReportQuery(q);
        }}
        showSnapshot={setFrozen}
        report={data}
      />
      <ErrorText>{error}</ErrorText>
      <p className="text-xs text-muted-foreground">
        {frozen ? "Saved snapshot" : "Current report"} · {String(data.from)} –{" "}
        {String(data.to)} · As of{" "}
        {data.as_of ? new Date(String(data.as_of)).toLocaleString() : "now"}
      </p>
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        {[
          ["placements", "Tracked placements", Globe2],
          ["publishers", "Publisher relationships", TrendingUp],
          ["live", "Pages available", ShieldCheck],
          ["work", "Open work", CheckCircle2],
        ].map(([k, title, Icon]) => (
          <button
            key={String(k)}
            className="rounded-xl border bg-card p-5 text-left"
            onClick={k === "work" ? onWork : undefined}
          >
            <span className="flex items-center justify-between text-sm text-muted-foreground">
              {String(title)}
              <Icon className="size-4" />
            </span>
            <span className="mt-3 block text-3xl font-semibold tabular-nums">
              {display(data[String(k)])}
            </span>
          </button>
        ))}
      </div>
      <PresenceMomentum
        momentum={data.momentum as Parameters<typeof PresenceMomentum>[0]["momentum"]}
        voice={data.share_of_voice as Parameters<typeof PresenceMomentum>[0]["voice"]}
      />
      <div className="grid gap-5 lg:grid-cols-3">
        <section className="rounded-xl border p-5 lg:col-span-2">
          <h2 className="font-semibold">Coverage over time</h2>
          <p className="mt-1 text-xs text-muted-foreground">
            Discovery, claimed publication and first verification · selected
            months in the selected period
          </p>
          <div
            className="mt-6 flex h-44 items-end gap-2"
            role="img"
            aria-label="Monthly discovery, publication and verification counts"
          >
            {growth.map((g) => (
              <div
                key={g.month}
                className="flex h-full flex-1 flex-col justify-end gap-2"
              >
                <div className="flex h-36 items-end justify-center gap-0.5">
                  {(["discovered", "published", "first_verified"] as const).map(
                    (k, i) => (
                      <div
                        key={k}
                        title={`${g.month}: ${g[k]} ${label(k)}`}
                        className={`w-2/5 rounded-t-sm ${["bg-blue-400", "bg-violet-400", "bg-emerald-400"][i]}`}
                        style={{
                          height: `${(g[k] / maximum) * 100}%`,
                          minHeight: g[k] ? 2 : 0,
                        }}
                      />
                    ),
                  )}
                </div>
                <span className="text-center text-[10px] text-muted-foreground">
                  {new Date(g.month).toLocaleDateString(undefined, {
                    month: "short",
                    timeZone: "UTC",
                  })}
                </span>
              </div>
            ))}
          </div>
          <div className="mt-4 flex flex-wrap gap-4 text-xs">
            <span className="text-blue-400">● Discovered</span>
            <span className="text-violet-400">● Published</span>
            <span className="text-emerald-400">● First verified</span>
          </div>
        </section>
        <section className="space-y-4 rounded-xl border p-5">
          <h2 className="font-semibold">Health and follow-up</h2>
          <div className="flex justify-between text-sm">
            <span>Pages available</span>
            <span>{display(data.live)}</span>
          </div>
          <div className="flex justify-between text-sm">
            <span>Confirmed unavailable</span>
            <span>{display(data.lost)}</span>
          </div>
          <div className="flex justify-between text-sm">
            <span>Needs verification</span>
            <span>{display(data.unverified)}</span>
          </div>
          <Button variant="ghost" onClick={onWork}>
            Open work queue
            <ArrowUpRight className="size-3.5" />
          </Button>
          <p className="flex gap-2 text-xs text-muted-foreground">
            <CircleHelp className="size-4 shrink-0" />
            Available pages may still need a content or relationship review.
          </p>
        </section>
      </div>
      <div className="grid gap-5 md:grid-cols-2">
        {[
          ["by_kind", "Placement mix"],
          ["by_arrangement", "How coverage was acquired"],
        ].map(([k, title]) => (
          <section key={k} className="rounded-xl border p-5">
            <h2 className="mb-4 font-semibold">{title}</h2>
            {(data[k] as Record<string, unknown>[]).length ? (
              (data[k] as Record<string, unknown>[]).map((r) => (
                <button
                  key={String(r.kind ?? r.arrangement)}
                  className="mb-3 flex w-full items-center gap-3 text-left text-sm enabled:hover:text-brand disabled:cursor-default"
                  disabled={!!frozen}
                  title={frozen ? "Saved snapshot" : "View matching placements"}
                  onClick={() =>
                    onPlacements(
                      {
                        [k === "by_kind" ? "kind" : "arrangement"]: String(
                          r.kind ?? r.arrangement,
                        ),
                      },
                      data.filters
                        ? (JSON.parse(String(data.filters)) as FilterGroup)
                        : emptyFilters,
                    )
                  }
                >
                  <span className="w-28 text-muted-foreground">
                    {label(String(r.kind ?? r.arrangement))}
                  </span>
                  <div className="h-1.5 flex-1 rounded bg-muted">
                    <div
                      className="h-full rounded bg-brand"
                      style={{
                        width: `${(Number(r.count) / Math.max(1, Number(data.placements))) * 100}%`,
                      }}
                    />
                  </div>
                  <span>{display(r.count)}</span>
                </button>
              ))
            ) : (
              <p className="text-sm text-muted-foreground">
                Add placements to see your coverage mix.
              </p>
            )}
          </section>
        ))}
      </div>
      {finance && costs.length > 0 && (
        <section className="rounded-xl border p-5">
          <h2 className="mb-4 font-semibold">Commercial summary</h2>
          <DataTable
            data={costs}
            rowId={(r) => String(r.currency)}
            columns={[
              "currency",
              "committed",
              "invoiced",
              "paid",
              "budget",
            ].map((k) => ({
              accessorKey: k,
              header: label(k),
              cell: ({
                row,
              }: {
                row: { original: Record<string, unknown> };
              }) =>
                k === "currency"
                  ? String(row.original.currency)
                  : money(row.original[k], row.original.currency),
            }))}
          />
          <p className="mt-3 text-xs text-muted-foreground">
            Currencies remain separate. Commitments, invoices and payments are
            different views of the same spend; they are never added together.
          </p>
        </section>
      )}
      {detailed && analytics && !frozen && (
        <CoverageOutcomes
          brand={brand}
          from={String(data.from)}
          to={String(data.to)}
        />
      )}
      {detailed && (
        <DataTable
          data={growth}
          rowId={(r) => r.month}
          columns={[
            "month",
            "discovered",
            "published",
            "first_verified",
            "baseline_verified",
            "lost",
            "regained",
            "last_confirmed_available",
          ].map((k) => ({ accessorKey: k, header: label(k) }))}
        />
      )}
      {detailed && (
        <div className="grid gap-5 md:grid-cols-2">
          {(
            [
              [
                "publisher_concentration",
                "Publisher concentration",
                ["domain", "placements"],
              ],
              ["by_stage", "Current pipeline", ["stage", "count"]],
              [
                "retention",
                "Verified retention cohorts",
                [
                  "cohort",
                  "days",
                  "eligible",
                  "available",
                  "gone",
                  "unverified",
                ],
              ],
              ["by_sentiment", "Recorded sentiment", ["sentiment", "count"]],
              [
                "reported_delivery",
                "Reported delivery by source",
                [
                  "source",
                  "reports",
                  "impressions",
                  "clicks",
                  "sessions",
                  "leads",
                  "conversions",
                ],
              ],
            ] as [string, string, string[]][]
          ).map(([key, title, columns]) => (
            <section key={key} className="min-w-0 rounded-xl border p-5">
              <h2 className="mb-3 font-semibold">{title}</h2>
              <DataTable
                data={(data[key] ?? []) as Record<string, unknown>[]}
                rowId={(r) => String(r[columns[0]])}
                columns={columns.map((k) => ({
                  accessorKey: k,
                  header: label(k),
                  cell: ({
                    row,
                  }: {
                    row: { original: Record<string, unknown> };
                  }) => display(row.original[k]),
                }))}
              />
              {key === "reported_delivery" && (
                <p className="mt-3 text-xs text-muted-foreground">
                  Source-reported counts for periods fully inside this range.
                  Unknowns are blank; overlapping source reports may overlap.
                  These are not causal attribution.
                </p>
              )}
            </section>
          ))}
        </div>
      )}
      <p className="text-xs text-muted-foreground">
        {String(data.measurement_note)}
      </p>
    </div>
  );
}
function CoverageImport({
  ws,
  brand,
  definition,
  canCreate,
  done,
}: {
  ws: string;
  brand: string;
  definition?: Definition;
  canCreate: boolean;
  done: () => void;
}) {
  const [rawRows, setRawRows] = useState<Record<string, string>[]>([]),
    [mapping, setMapping] = useState<Record<string, string>>({}),
    [publisher, setPublisher] = useState(""),
    [baseline, setBaseline] = useState(true),
    [prepared, setPrepared] = useState<Record<string, unknown>[]>([]),
    [results, setResults] = useState<Record<string, unknown>[]>([]),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const fields = definition?.fields ?? [];
  const invalidate = () => {
    setPrepared([]);
    setResults([]);
    setError("");
  };
  const normalizeDomain = (raw: string) => {
    try {
      return new URL(raw.includes("://") ? raw : `https://${raw}`).hostname
        .toLowerCase()
        .replace(/^www\./, "");
    } catch {
      return "";
    }
  };
  const prepare = async () => {
    const targets = Object.values(mapping).filter(Boolean);
    if (new Set(targets).size !== targets.length)
      throw new Error("Match each placement field to only one CSV column.");
    const domains = new Map<string, string>();
    const rows: Record<string, unknown>[] = [];
    for (const raw of rawRows) {
      const row: Record<string, unknown> = { baseline };
      let domain = "";
      let domainProvided = false;
      for (const [header, target] of Object.entries(mapping)) {
        const value = raw[header]?.trim();
        if (!target || !value) continue;
        if (target === "publisher_domain") {
          domainProvided = true;
          domain = normalizeDomain(value);
          continue;
        }
        const f = fields.find((f) => f.name === target);
        row[target] =
          f?.kind === "bool"
            ? /^(true|yes|1)$/i.test(value)
              ? true
              : /^(false|no|0)$/i.test(value)
                ? false
                : value
            : f?.kind === "hours"
              ? Number(value)
              : value;
      }
      if (!row.publisher_id && domain) {
        if (!domains.has(domain)) {
          const r = await list(brand, "publishers", { q: domain, limit: 100 });
          domains.set(
            domain,
            r.data.find((p) => normalizeDomain(String(p.domain)) === domain)
              ?.id ?? "",
          );
        }
        row.publisher_id = domains.get(domain) || undefined;
      }
      if (!row.publisher_id && !domainProvided && publisher)
        row.publisher_id = publisher;
      rows.push(row);
    }
    return rows;
  };
  const run = async (commit: boolean) => {
    setBusy(true);
    setError("");
    try {
      const rows = commit ? prepared : await prepare();
      const { data, error } = await api.POST(
        "/v1/brands/{brand}/coverage/import",
        { params: { path: { brand } }, body: { rows: rows as never, commit } },
      );
      if (error || !data) throw new Error(errorMessage(error));
      if (!("data" in data))
        throw new Error(
          "This import is waiting for mission approval. No rows have been imported yet.",
        );
      setResults(
        (data.data as Record<string, unknown>[]).map((result) => {
          const row = rows[Number(result.row) - 1];
          return row && !row.publisher_id
            ? {
                ...result,
                error:
                  "No publisher matched. Choose one for rows without a website, or add the matching website under Publishers.",
              }
            : result;
        }),
      );
      setPrepared(commit ? [] : rows);
      if (commit) done();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <fieldset disabled={busy} className="space-y-5 p-5">
      <p className="text-sm text-muted-foreground">
        Upload a CSV with a title for each placement. Match its columns below.
        Choose one publisher for the file, or map a Publisher website column to
        match existing websites.
      </p>
      <div className="space-y-2">
        <label htmlFor="coverage-publisher_id" className="text-sm font-medium">
          Default publisher
        </label>
        <ReferencePicker
          ws={ws}
          brand={brand}
          field={{
            name: "publisher_id",
            kind: "uuid",
            reference: "publishers",
            required: false,
            nullable: false,
            choices: [],
            default: "",
          }}
          value={publisher}
          onChange={(v) => {
            setPublisher(v);
            invalidate();
          }}
          allowCreate={canCreate}
        />
      </div>
      <label className="flex gap-2 text-sm">
        <input
          type="checkbox"
          checked={baseline}
          onChange={(e) => {
            setBaseline(e.target.checked);
            invalidate();
          }}
        />
        Treat imported placements as existing coverage
      </label>
      <p className="text-xs text-muted-foreground">
        Existing coverage is a historical baseline. Importing it does not
        establish when it was published.
      </p>
      <input
        type="file"
        accept=".csv,text/csv"
        aria-label="Choose placements CSV"
        onChange={async (e) => {
          invalidate();
          setRawRows([]);
          try {
            const file = e.target.files?.[0];
            if (!file) return;
            if (file.size > 1024 * 1024)
              throw new Error("Choose a CSV smaller than 1 MB.");
            const rows = parseCSV(await file.text());
            if (rows.length > 200)
              throw new Error("Import up to 200 rows at a time.");
            const aliases: Record<string, string> = {
              name: "title",
              placement: "title",
              page: "url",
              pageurl: "url",
              website: "publisher_domain",
              publisherwebsite: "publisher_domain",
              publisherdomain: "publisher_domain",
              format: "kind",
              acquisition: "arrangement",
              published: "published_on",
              expectedlive: "expected_on",
            };
            const next: Record<string, string> = {};
            for (const header of Object.keys(rows[0] ?? {})) {
              const normalized = header.toLowerCase().replace(/[^a-z0-9]/g, "");
              next[header] =
                fields.find((f) => f.name.replaceAll("_", "") === normalized)
                  ?.name ??
                aliases[normalized] ??
                "";
            }
            setMapping(next);
            setRawRows(rows);
          } catch (e) {
            setError(String(e));
          }
        }}
      />
      {rawRows.length > 0 && (
        <section className="space-y-3 rounded-lg border p-4">
          <h3 className="font-medium">Match columns</h3>
          {Object.entries(mapping).map(([header, value]) => (
            <label
              key={header}
              className="grid items-center gap-2 text-sm sm:grid-cols-2"
            >
              <span>
                {header}
                <span className="block truncate text-xs text-muted-foreground">
                  {rawRows[0]?.[header]}
                </span>
              </span>
              <select
                aria-label={`Map ${header}`}
                className={field}
                value={value}
                onChange={(e) => {
                  setMapping((m) => ({ ...m, [header]: e.target.value }));
                  invalidate();
                }}
              >
                <option value="">Skip column</option>
                <option value="publisher_domain">Publisher website</option>
                {fields.map((f) => (
                  <option key={f.name} value={f.name}>
                    {label(f.name.replace(/_id$/, ""))}
                  </option>
                ))}
              </select>
            </label>
          ))}
        </section>
      )}
      <ErrorText>{error}</ErrorText>
      <div className="flex gap-2">
        <Button
          disabled={!rawRows.length || busy}
          variant="ghost"
          onClick={() => run(false)}
        >
          Preview {rawRows.length || ""} rows
        </Button>
        <Button disabled={!prepared.length || busy} onClick={() => run(true)}>
          Import valid rows
        </Button>
      </div>
      {results.length > 0 && (
        <DataTable
          data={results}
          rowId={(r) => String(r.row)}
          columns={["row", "status", "error"].map((k) => ({
            accessorKey: k,
            header: label(k),
          }))}
        />
      )}
    </fieldset>
  );
}

function CoverageRelationshipContext({
  ws,
  brand,
  record,
  publisherID,
  allowed,
}: {
  ws: string;
  brand: string;
  record?: string;
  publisherID?: string;
  allowed: boolean;
}) {
  const [items, setItems] = useState<Record<string, unknown>[]>([]),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(true),
    [company, setCompany] = useState(record),
    [next, setNext] = useState<string | null>(null);
  useEffect(() => {
    let active = true;
    setItems([]);
    setCompany(record);
    setNext(null);
    setError("");
    setLoading(true);
    const run = async () => {
      try {
        let id = record;
        if (!id && publisherID) {
          const r = await api.GET(
            "/v1/brands/{brand}/coverage/publishers/{id}",
            { params: { path: { brand, id: publisherID } } },
          );
          if (r.error) throw new Error(errorMessage(r.error));
          id = r.data?.record_id;
        }
        if (!id || !allowed) {
          if (active) setLoading(false);
          return;
        }
        const r = await api.GET(
          "/v1/brands/{brand}/records/{record}/timeline",
          {
            params: {
              path: { brand, record: id },
              query: { limit: 20, people: "true" },
            },
          },
        );
        if (r.error) throw new Error(errorMessage(r.error));
        if (active) {
          setCompany(id);
          setItems(
            (r.data?.data ?? []) as unknown as Record<string, unknown>[],
          );
          setNext(r.data?.next_cursor ?? null);
          setLoading(false);
        }
      } catch (e) {
        if (active) {
          setError(String(e));
          setLoading(false);
        }
      }
    };
    void run();
    return () => {
      active = false;
    };
  }, [ws, brand, record, publisherID, allowed]);
  if (!allowed)
    return (
      <p className="text-sm text-muted-foreground">
        CRM access is required to read relationship history.
      </p>
    );
  return (
    <section className="space-y-3 rounded-lg border p-4">
      <h3 className="font-medium">Previous relationship activity</h3>
      <p className="text-xs text-muted-foreground">
        Includes activity beyond this placement and the company's linked
        contacts, within your CRM access.
      </p>
      <ErrorText>{error}</ErrorText>
      {loading ? (
        <Loading />
      ) : items.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          No visible activity found. This does not prove there has never been
          contact.
        </p>
      ) : (
        <ol className="divide-y">
          {items.map((it, i) => (
            <li key={String(it.id) + i} className="space-y-1 py-3">
              <div className="flex justify-between gap-3 text-xs text-muted-foreground">
                <span>{label(String(it.kind))}</span>
                <time>{new Date(String(it.at)).toLocaleDateString()}</time>
              </div>
              <p className="text-sm">{display(it.title)}</p>
            </li>
          ))}
        </ol>
      )}
      {company && (
        <a
          className="text-sm text-brand underline"
          href={appPath(ws, "people", company)}
        >
          Open complete CRM context
        </a>
      )}
      {next && company && (
        <Button
          variant="ghost"
          onClick={async () => {
            try {
              const r = await api.GET(
                "/v1/brands/{brand}/records/{record}/timeline",
                {
                  params: {
                    path: { brand, record: company },
                    query: { limit: 20, cursor: next, people: "true" },
                  },
                },
              );
              if (r.error) throw new Error(errorMessage(r.error));
              setItems((old) => [
                ...old,
                ...((r.data?.data ?? []) as unknown as Record<
                  string,
                  unknown
                >[]),
              ]);
              setNext(r.data?.next_cursor ?? null);
            } catch (e) {
              setError(String(e));
            }
          }}
        >
          Earlier activity
        </Button>
      )}
    </section>
  );
}

function formGroups(def: Definition): [string, Field[]][] {
  if (def.name !== "placements") return [["Details", def.fields]];
  const groups: [string, string[]][] = [
    [
      "Placement",
      [
        "title",
        "url",
        "inventory",
        "publisher_id",
        "kind",
        "arrangement",
        "stage",
      ],
    ],
    [
      "Relationship and campaign",
      [
        "contact_record_id",
        "delivered_by",
        "deal_id",
        "owner_id",
        "campaign",
        "market",
        "language",
        "tags",
        "notes",
      ],
    ],
    [
      "Dates and commercial terms",
      ["agreement_id", "expected_on", "published_on", "expires_on", "baseline"],
    ],
    [
      "Content and monitoring",
      [
        "target_url",
        "expected_link",
        "expected_rel",
        "expected_mention",
        "sentiment",
        "monitor",
        "reader",
        "check_every_hours",
        "monitor_accuracy",
      ],
    ],
  ];
  return groups.map(([title, names]) => [
    title,
    names
      .map((n) => def.fields.find((f) => f.name === n))
      .filter((f): f is Field => !!f),
  ]);
}
function CoverageRelated({
  brand,
  resource,
  filter,
  open,
}: {
  brand: string;
  resource: Resource;
  filter: Record<string, string>;
  open: (r: Resource, row: Row) => void;
}) {
  const [rows, setRows] = useState<Row[]>([]),
    [error, setError] = useState("");
  const key = JSON.stringify(filter);
  useEffect(() => {
    let active = true;
    list(brand, resource, filter)
      .then((r) => {
        if (active) setRows(r.data);
      })
      .catch((e) => {
        if (active) setError(String(e));
      });
    return () => {
      active = false;
    };
  }, [brand, resource, key]);
  return (
    <div>
      <ErrorText>{error}</ErrorText>
      {rows.length ? (
        rows.map((r) => (
          <button
            className="block w-full border-b py-3 text-left text-sm text-brand"
            key={r.id}
            onClick={() => open(resource, r)}
          >
            {nameOf(r)}
          </button>
        ))
      ) : (
        <p className="text-sm text-muted-foreground">
          No linked {label(resource).toLowerCase()} yet.
        </p>
      )}
    </div>
  );
}
function CoverageRules({
  brand,
  defs,
  canManage,
  refresh,
}: {
  brand: string;
  defs: Definition[];
  canManage: boolean;
  refresh: () => void;
}) {
  const [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const install = async () => {
    setBusy(true);
    try {
      const existing = await list(brand, "rules");
      const kinds =
        defs
          .find((d) => d.name === "rules")
          ?.fields.find((f) => f.name === "kind")?.choices ?? [];
      for (const kind of kinds) {
        if (existing.data.some((r) => r.kind === kind)) continue;
        await save(brand, "rules", { title: label(kind), kind, enabled: true });
      }
      refresh();
      toast("Maintenance settings added");
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section className="space-y-3 rounded-xl border bg-muted/20 p-5">
      <h2 className="font-semibold">Evidence creates work for your team</h2>
      <p className="text-sm text-muted-foreground">
        Page loss and link problems need two agreeing checks. First-live and
        earned-relationship tasks appear on the first verified read. Publication
        deadlines use the promised date. Accuracy findings need a person to
        review the cited claim. These defaults are enabled; create their
        settings to assign owners or disable individual triggers.
      </p>
      <p className="text-sm text-muted-foreground">
        A relationship task asks you to review existing conversations and
        preferences before reaching out. Suppressed publishers receive no new
        introduction task. Commercial deadlines appear under Commercial →
        Commercial work. Rules create work; they do not send messages or make
        purchases.
      </p>
      <ErrorText>{error}</ErrorText>
      {canManage && (
        <Button disabled={busy} onClick={install}>
          {busy ? "Adding settings…" : "Configure maintenance defaults"}
        </Button>
      )}
    </section>
  );
}
function CoverageSettings({ go }: { go: Props["go"]; can: Props["can"] }) {
  return (
    <div className="grid gap-5 md:grid-cols-2">
      {[
        [
          "Source connections",
          "Use your own Geonode account for Google discovery and rendered page reads. Ahrefs and DataForSEO feed backlink discovery through the existing SEO sources.",
          "connectors",
        ],
        [
          "Knowledge and AI",
          "Publish customer-facing pricing and product facts in Knowledge. Accuracy reviews cite the fact version; changed or withdrawn facts invalidate old findings.",
          "knowledge",
        ],
        [
          "Discovery schedule",
          "Saved searches set query, language and cadence. Connection limits bound provider usage. A failed or capped source remains visible in its search history.",
          "news",
        ],
        [
          "Workspace permissions",
          "Coverage access, commercial access and agreement approval are separate. Team views never expand the reader's permissions.",
          "settings",
        ],
      ].map(([title, description, tab]) => (
        <section key={title} className="rounded-xl border p-5">
          <h2 className="font-semibold">{title}</h2>
          <p className="my-3 text-sm text-muted-foreground">{description}</p>
          <Button variant="ghost" onClick={() => go(tab as Tab)}>
            Open {title.toLowerCase()}
            <ArrowUpRight className="size-3" />
          </Button>
        </section>
      ))}
      <section className="rounded-xl border p-5">
        <h2 className="font-semibold">Monitoring controls</h2>
        <p className="mt-3 text-sm text-muted-foreground">
          Each placement has a reader, check interval and pause switch. Native
          reads respect robots and site opt-outs. Connected reads require your
          workspace's connection and budget. A missing key, blocked page or
          network failure does not establish a lost placement.
        </p>
      </section>
    </div>
  );
}
function CoverageSearches({
  ws,
  brand,
  canManage,
}: {
  ws: string;
  brand: string;
  canManage: boolean;
}) {
  const [rows, setRows] = useState<import("@outflare/sdk").NewsSearch[]>([]),
    [query, setQuery] = useState(""),
    [cadence, setCadence] = useState(168),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [reload, setReload] = useState(0);
  useEffect(() => {
    let active = true;
    api
      .GET("/v1/workspaces/{ws}/news-searches", {
        params: { path: { ws }, query: { brand } },
      })
      .then(({ data, error }) => {
        if (!active) return;
        if (error) setError(errorMessage(error));
        else setRows(data?.data ?? []);
      })
      .catch((e) => active && setError(String(e)));
    return () => {
      active = false;
    };
  }, [ws, brand, reload]);
  const create = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const r = await api.POST("/v1/workspaces/{ws}/news-searches", {
        params: { path: { ws } },
        body: {
          brand_id: brand,
          name: query,
          query,
          purpose: "coverage",
          check_every_hours: cadence,
        },
      });
      if (r.error) throw new Error(errorMessage(r.error));
      setQuery("");
      setReload((n) => n + 1);
      toast("Discovery search saved");
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <details className="rounded-xl border p-4">
      <summary className="cursor-pointer font-medium">
        News searches · {rows.length}
      </summary>
      <div className="mt-4 space-y-4">
        <p className="text-sm text-muted-foreground">
          Search Google through your workspace's Geonode account. Use quoted
          brand names, product aliases or comparison queries. Review the matches
          before confirming placements.
        </p>
        <ErrorText>{error}</ErrorText>
        {rows.map((r) => (
          <div
            key={r.id}
            className="flex flex-wrap items-center gap-3 border-b pb-3"
          >
            <div className="flex-1 text-sm">
              <strong>{r.name}</strong>
              <p className="text-muted-foreground">
                {r.query} · {r.status} · every {r.check_every_hours} hours
              </p>
              {r.last_error_code && (
                <p className="text-amber-500">{label(r.last_error_code)}</p>
              )}
            </div>
            {canManage && (
              <>
                <Button
                  variant="ghost"
                  onClick={async () => {
                    const result = await api.POST(
                      "/v1/workspaces/{ws}/news-searches/{search}/run",
                      { params: { path: { ws, search: r.id } } },
                    );
                    if (result.error) setError(errorMessage(result.error));
                    else
                      toast(
                        "Search queued; refresh discoveries after it finishes",
                      );
                  }}
                >
                  Run now
                </Button>
                <Button
                  variant="ghost"
                  onClick={async () => {
                    const result = await api.PATCH(
                      "/v1/workspaces/{ws}/news-searches/{search}",
                      {
                        params: { path: { ws, search: r.id } },
                        body: {
                          status: r.status === "active" ? "paused" : "active",
                        },
                      },
                    );
                    if (result.error) setError(errorMessage(result.error));
                    else setReload((n) => n + 1);
                  }}
                >
                  {r.status === "active" ? "Pause" : "Resume"}
                </Button>
              </>
            )}
          </div>
        ))}
        {canManage && (
          <form onSubmit={create} className="flex flex-wrap gap-2">
            <input
              className={`${field} flex-1`}
              aria-label="Discovery search query"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder='"Your brand" review OR comparison'
              required
            />
            <select
              className={`${field} !w-auto`}
              aria-label="Search frequency"
              value={cadence}
              onChange={(e) => setCadence(Number(e.target.value))}
            >
              <option value={24}>Daily</option>
              <option value={168}>Weekly</option>
              <option value={720}>Monthly</option>
            </select>
            <Button type="submit" disabled={busy}>
              Save search
            </Button>
          </form>
        )}
      </div>
    </details>
  );
}

function CoverageReportTools({
  report,
  brand,
  manage,
  definition,
  query,
  frozen,
  apply,
  showSnapshot,
}: {
  report: Record<string, unknown>;
  brand: string;
  manage: boolean;
  definition?: Definition;
  query: { from?: string; to?: string; filters?: string };
  frozen: boolean;
  apply: (q: { from?: string; to?: string; filters?: string }) => void;
  showSnapshot: (r: Record<string, unknown>) => void;
}) {
  const [from, setFrom] = useState(query.from ?? ""),
    [to, setTo] = useState(query.to ?? ""),
    [filters, setFilters] = useState<FilterGroup>(emptyFilters),
    [title, setTitle] = useState(""),
    [snapshots, setSnapshots] = useState<Record<string, unknown>[]>([]),
    [error, setError] = useState("");
  const read = () =>
    api
      .GET("/v1/brands/{brand}/coverage/report-snapshots", {
        params: { path: { brand } },
      })
      .then((r) => {
        if (r.error) setError(errorMessage(r.error));
        else setSnapshots(r.data?.data ?? []);
      });
  useEffect(() => {
    void read().catch((e) => setError(String(e)));
  }, [brand]);
  const period = (days: number) => {
    const end = new Date(),
      start = new Date();
    start.setUTCDate(end.getUTCDate() - days + 1);
    const f = start.toISOString().slice(0, 10),
      t = end.toISOString().slice(0, 10);
    setFrom(f);
    setTo(t);
    apply({ from: f, to: t, filters: JSON.stringify(filters) });
    track("ui.coverage_filters_changed", { resource: "reports" });
  };
  return (
    <section className="space-y-3 rounded-xl border p-4">
      <div className="flex flex-wrap items-center gap-2">
        {[7, 30, 90, 365].map((d) => (
          <Button key={d} variant="ghost" onClick={() => period(d)}>
            {d === 365 ? "Year" : `${d} days`}
          </Button>
        ))}
        <input
          className={`${field} !w-auto`}
          type="date"
          aria-label="Report from"
          value={from}
          onChange={(e) => setFrom(e.target.value)}
        />
        <span>to</span>
        <input
          className={`${field} !w-auto`}
          type="date"
          aria-label="Report to"
          value={to}
          onChange={(e) => setTo(e.target.value)}
        />
        <Button
          onClick={() => apply({ from, to, filters: JSON.stringify(filters) })}
        >
          Apply period & filters
        </Button>
        {frozen && <Badge tone="blue">Immutable snapshot</Badge>}
      </div>
      {definition && (
        <FilterBuilder
          definition={definition}
          value={filters}
          onChange={setFilters}
        />
      )}
      <ErrorText>{error}</ErrorText>
      <div className="flex flex-wrap gap-2">
        <select
          className={`${field} max-w-xs`}
          aria-label="Saved report"
          defaultValue=""
          onChange={async (e) => {
            if (!e.target.value) return;
            try {
              const r = await api.GET(
                "/v1/brands/{brand}/coverage/report-snapshots/{id}",
                { params: { path: { brand, id: e.target.value } } },
              );
              if (r.error) throw new Error(errorMessage(r.error));
              if (r.data?.data)
                showSnapshot(r.data.data as Record<string, unknown>);
            } catch (err) {
              setError(String(err));
            }
          }}
        >
          <option value="">Open saved report…</option>
          {snapshots.map((s) => (
            <option key={String(s.id)} value={String(s.id)}>
              {String(s.title)} ·{" "}
              {new Date(String(s.created_at)).toLocaleDateString()}
            </option>
          ))}
        </select>
        {manage && !frozen && (
          <>
            <input
              className={`${field} max-w-xs`}
              aria-label="Report title"
              placeholder="Name this report snapshot"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
            />
            <Button
              disabled={!title.trim()}
              variant="ghost"
              onClick={async () => {
                try {
                  const r = await api.POST(
                    "/v1/brands/{brand}/coverage/report-snapshots",
                    { params: { path: { brand } }, body: { title, ...query } },
                  );
                  if (r.error) throw new Error(errorMessage(r.error));
                  setTitle("");
                  await read();
                  toast("Report snapshot saved");
                } catch (e) {
                  setError(String(e));
                }
              }}
            >
              Save snapshot
            </Button>
          </>
        )}
        <Button
          variant="ghost"
          onClick={async () => {
            try {
              const blob = new Blob([JSON.stringify(report, null, 2)], {
                  type: "application/json",
                }),
                u = URL.createObjectURL(blob),
                a = document.createElement("a");
              a.href = u;
              a.download = "coverage-report.json";
              a.click();
              URL.revokeObjectURL(u);
              track("ui.coverage_exported", { resource: "reports" });
            } catch (e) {
              setError(String(e));
            }
          }}
        >
          Export displayed report
        </Button>
      </div>
    </section>
  );
}

function CoverageDraft({ subject, body }: { subject: string; body: string }) {
  const [editedSubject, setSubject] = useState(subject),
    [editedBody, setBody] = useState(body);
  return (
    <div className="space-y-2 rounded-lg border p-3">
      <h4 className="text-sm font-medium">Draft for review</h4>
      <input
        className={field}
        aria-label="Draft subject"
        value={editedSubject}
        onChange={(e) => setSubject(e.target.value)}
      />
      <textarea
        className={field}
        rows={7}
        aria-label="Draft message"
        value={editedBody}
        onChange={(e) => setBody(e.target.value)}
      />
      <CopyButton
        label="Copy reviewed draft"
        text={`${editedSubject}\n\n${editedBody}`}
      />
      <p className="text-xs text-muted-foreground">
        Review names, facts, prior conversations and consent. Copying does not
        send this draft. Local edits are retained while this drawer stays open.
      </p>
    </div>
  );
}
function currencyDigits(currency: string) {
  try {
    return (
      new Intl.NumberFormat(undefined, {
        style: "currency",
        currency,
      }).resolvedOptions().maximumFractionDigits ?? 2
    );
  } catch {
    return 2;
  }
}
function parseMoney(value: string, currency: string) {
  const decimals = currencyDigits(currency),
    parts = value.split(".");
  if (
    !/^\d+(\.\d*)?$/.test(value) ||
    parts.length > 2 ||
    (parts[1]?.length ?? 0) > decimals
  )
    throw new Error(
      `Enter an amount with at most ${decimals} decimal places for ${currency}.`,
    );
  const minor = Number(parts[0] + (parts[1] ?? "").padEnd(decimals, "0"));
  if (!Number.isSafeInteger(minor) || minor > 9000000000000000)
    throw new Error("Amount is too large.");
  return minor;
}

function CoverageCreateReference({
  ws,
  brand,
  field: f,
  onCreated,
}: {
  ws: string;
  brand: string;
  field: Field;
  onCreated: (r: Row) => void;
}) {
  const [open, setOpen] = useState(false),
    [name, setName] = useState(""),
    [company, setCompany] = useState(""),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const publisher = f.reference === "publishers";
  const create = async () => {
    setBusy(true);
    try {
      if (publisher) {
        onCreated(
          await save(brand, "publishers", { domain: name, record_id: company }),
        );
      } else {
        const r = await api.POST("/v1/workspaces/{ws}/records", {
          params: { path: { ws } },
          body: {
            type: f.name === "record_id" ? "account" : "person",
            name,
            brand_id: brand,
          },
        });
        if (r.error) throw new Error(errorMessage(r.error));
        if (r.response.status === 202)
          throw new Error("CRM creation is waiting for mission approval.");
        if (r.data) onCreated({ ...r.data, title: name, version: 1 } as Row);
      }
      setOpen(false);
      setName("");
      toast(publisher ? "Publisher added" : "CRM record created");
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div>
      {!open ? (
        <Button type="button" variant="ghost" onClick={() => setOpen(true)}>
          <Plus className="size-3" />
          Create{" "}
          {publisher
            ? "publisher"
            : f.name === "record_id"
              ? "CRM company"
              : "CRM contact"}
        </Button>
      ) : (
        <div className="space-y-3 rounded-lg border p-3">
          <ErrorText>{error}</ErrorText>
          <input
            className={field}
            aria-label={
              publisher ? "New publisher domain" : "New CRM record name"
            }
            placeholder={publisher ? "publisher.example" : "Name"}
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          {publisher && (
            <ReferencePicker
              ws={ws}
              brand={brand}
              field={{
                name: "record_id",
                kind: "uuid",
                reference: "records",
                required: true,
                nullable: false,
                choices: [],
                default: null,
              }}
              value={company}
              onChange={setCompany}
              allowCreate
            />
          )}
          <div className="flex gap-2">
            <Button
              type="button"
              disabled={busy || !name.trim() || (publisher && !company)}
              onClick={create}
            >
              {busy ? "Creating…" : "Create and select"}
            </Button>
            <Button
              type="button"
              variant="ghost"
              onClick={() => setOpen(false)}
            >
              Cancel
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
