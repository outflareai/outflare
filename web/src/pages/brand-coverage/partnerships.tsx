// Partnerships: a board per partnership pipeline (SEO links, sponsorships,
// integrations, affiliates, comparison sites). Each pipeline is a CRM
// record type a setup kit adds to the brand, with its own stages and
// fields, so a partnership is a CRM record with its history, linked to the
// partner's company. A pipeline that isn't set up yet offers "Set up";
// "Add prospect" takes a website and finds or adds the company.
import { useState, type FormEvent } from "react";
import { api, errorMessage } from "../../api";
import { Button, Empty, ErrorText, field, Loading, toast } from "../../ui";
import { RecordBoard } from "../crm/Board";
import { useCatalog } from "@/lib/records";
import { track } from "@/lib/analytics";

export const pipelines = [
  {
    kit: "partnerships-seo",
    type: "link_partnership",
    label: "SEO links",
    blurb: "Guest posts, link inserts, resource pages and mentions, from first pitch to a live link.",
  },
  {
    kit: "partnerships-sponsorships",
    type: "sponsorship",
    label: "Sponsorships",
    blurb: "Paid newsletter, podcast, creator, event and display placements, from media kit to results.",
  },
  {
    kit: "partnerships-integrations",
    type: "integration_partnership",
    label: "Integrations",
    blurb: "Technology partners, from scoping an integration to its listing going live.",
  },
  {
    kit: "partnerships-affiliates",
    type: "affiliate_partnership",
    label: "Affiliates",
    blurb: "Affiliates, from first contact to their first referred sale. Joining your partner program moves them to Signed up, and their first sale to Active.",
  },
  {
    kit: "partnerships-comparison-sites",
    type: "comparison_listing",
    label: "Comparison sites",
    blurb: "Sites that test and rank tools like yours, from a trial account to being listed and ranked.",
  },
] as const;

type Pipeline = (typeof pipelines)[number];

export function Partnerships(props: {
  ws: string;
  brand: string;
  can: (scope: string) => boolean;
  openRecord: (id: string) => void;
}) {
  // The catalog is read once per mount; setting up a pipeline remounts it.
  const [generation, setGeneration] = useState(0);
  return <PartnershipsBoards key={generation} {...props} reload={() => setGeneration((n) => n + 1)} />;
}

function PartnershipsBoards({
  ws,
  brand,
  can,
  openRecord,
  reload,
}: {
  ws: string;
  brand: string;
  can: (scope: string) => boolean;
  openRecord: (id: string) => void;
  reload: () => void;
}) {
  const cat = useCatalog(ws, brand);
  const [picked, setPicked] = useState<string>("");
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [tick, setTick] = useState(0);
  if (cat.error) return <ErrorText>{cat.error}</ErrorText>;
  if (!cat.catalog) return <Loading />;
  const installed = pipelines.filter((p) => cat.catalog?.types.some((t) => t.key === p.type && !t.archived));
  const missing = pipelines.filter((p) => !installed.includes(p));
  const current = installed.find((p) => p.type === picked) ?? installed[0];
  const setUp = async (p: Pipeline) => {
    setBusy(p.kit);
    setError("");
    try {
      const { data, error } = await api.POST("/v1/brands/{brand}/kits/{id}/apply", { params: { path: { brand, id: p.kit } }, body: {} });
      if (error) throw new Error(errorMessage(error));
      if (data && !("applied" in data)) {
        toast("Setting it up is waiting for mission approval.");
        return;
      }
      toast(`${p.label} pipeline is ready`);
      track("ui.partnership_pipeline_set_up", { kit: p.kit });
      setPicked(p.type);
      reload();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy("");
    }
  };
  return (
    <section className="space-y-4">
      <p className="max-w-3xl text-sm text-muted-foreground">
        Each kind of partnership has its own pipeline, stages and fields. Every partner is a CRM record linked to its company, so its history, emails and
        deals stay in one place. Drag a card to move it to another stage, or open it to fill in the details. When a partner goes live, with its page filled in, it's added to Placements and checked from then on; a booked sponsorship adds a proposed agreement.
      </p>
      <ErrorText>{error}</ErrorText>
      {installed.length > 0 && (
        <div className="flex flex-wrap items-center gap-2">
          {installed.map((p) => (
            <Button key={p.type} variant={current?.type === p.type ? "primary" : "ghost"} onClick={() => setPicked(p.type)}>
              {p.label}
            </Button>
          ))}
        </div>
      )}
      {current ? (
        <Pipeline
          key={current.type}
          ws={ws}
          brand={brand}
          pipeline={current}
          cat={cat}
          canManage={can("customers.manage")}
          openRecord={openRecord}
          tick={tick}
          changed={() => setTick((n) => n + 1)}
        />
      ) : (
        <Empty title="No partnership pipelines yet">Set one up below for each kind of partnership you work on.</Empty>
      )}
      {missing.length > 0 && (
        <div className="space-y-2">
          <h3 className="text-sm font-medium">{installed.length ? "More pipelines" : "Set up a pipeline"}</h3>
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {missing.map((p) => (
              <div key={p.kit} className="flex flex-col gap-2 rounded-lg border p-4">
                <span className="font-medium">{p.label}</span>
                <span className="flex-1 text-sm text-muted-foreground">{p.blurb}</span>
                {can("catalog.manage") ? (
                  <Button className="self-start" disabled={!!busy} onClick={() => setUp(p)}>
                    {busy === p.kit ? "Setting up…" : "Set up"}
                  </Button>
                ) : (
                  <span className="text-xs text-muted-foreground">An owner or admin can set this up.</span>
                )}
              </div>
            ))}
          </div>
        </div>
      )}
    </section>
  );
}

function Pipeline({
  ws,
  brand,
  pipeline,
  cat,
  canManage,
  openRecord,
  tick,
  changed,
}: {
  ws: string;
  brand: string;
  pipeline: Pipeline;
  cat: ReturnType<typeof useCatalog>;
  canManage: boolean;
  openRecord: (id: string) => void;
  tick: number;
  changed: () => void;
}) {
  const [website, setWebsite] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const stage = cat.fields(pipeline.type).find((f) => f.key === "stage");
  const add = async (e: FormEvent) => {
    e.preventDefault();
    if (!website.trim()) return;
    setBusy(true);
    setError("");
    try {
      const { data, error } = await api.POST("/v1/brands/{brand}/partnerships", {
        params: { path: { brand } },
        body: { type: pipeline.type, website: website.trim() },
      });
      if (error) throw new Error(errorMessage(error));
      if (data && "created" in data) {
        toast(data.created ? `Added ${data.record.name} to ${pipeline.label}` : `${data.record.name} is already in ${pipeline.label}`);
        setWebsite("");
        changed();
        if (!data.created) openRecord(data.record.id);
      } else toast("Adding it is waiting for mission approval.");
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };
  if (!stage) return <ErrorText>{pipeline.label} has no stage field.</ErrorText>;
  return (
    <div className="space-y-3">
      <p className="text-sm text-muted-foreground">{pipeline.blurb}</p>
      {canManage && (
        <form onSubmit={add} className="flex flex-wrap items-center gap-2">
          <input
            className={`${field} w-72`}
            placeholder="Partner's website, such as acme.com"
            aria-label="Partner's website"
            value={website}
            onChange={(e) => setWebsite(e.target.value)}
          />
          <Button type="submit" disabled={busy || !website.trim()}>
            {busy ? "Adding…" : "Add prospect"}
          </Button>
          <span className="text-xs text-muted-foreground">We find or add the company, and you fill in the rest on its card.</span>
        </form>
      )}
      <ErrorText>{error}</ErrorText>
      <RecordBoard
        ws={ws}
        type={pipeline.type}
        cat={cat}
        group={stage}
        config={{ q: "", sort: "updated_at", direction: "desc", filters: {} }}
        canManage={canManage}
        onOpen={openRecord}
        onChange={changed}
        tick={tick}
      />
    </div>
  );
}
