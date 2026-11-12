import { PickBrand } from "@/components/pick-brand";
import { DataTable } from "@/components/data-table";
import { useEffect, useState } from "react";
import { useBrand, pickedBrandId } from "@/lib/brand";
import type { components } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, Empty, ErrorText, Loading, PageHeader, Stat, Stats } from "../ui";
import type { Tab } from "../lib/paths";

type Share = components["schemas"]["AIAnswerShare"];
type Group = components["schemas"]["AIAnswerShareGroup"];
type Rival = components["schemas"]["AIAnswerShareRival"];

type Props = { ws: string; go: (tab: Tab, item?: string) => void };

const pct = (v: number | null | undefined) => (v == null ? "–" : `${(v * 100).toLocaleString(undefined, { maximumFractionDigits: 1 })}%`);
const num = (v: number) => v.toLocaleString();

export function AIAnswerShare(props: Props) {
  const { brand, active } = useBrand(props.ws);
  if (!brand) return <PickBrand what="AI answer share" brands={active.length} />;
  return <AIAnswerShareReport key={brand.id} {...props} />;
}

function AIAnswerShareReport({ ws, go }: Props) {
  const [data, setData] = useState<Share | null>(null);
  const [error, setError] = useState("");
  const [days, setDays] = useState(84);
  const brandKey = useBrand(ws).query.brand ?? "";

  useEffect(() => {
    setData(null);
    setError("");
    api.GET("/v1/brands/{brand}/ai-answer-share", { params: { path: { brand: pickedBrandId() }, query: { days } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setData(data);
    });
  }, [ws, brandKey, days]);

  const header = (
    <PageHeader
      title="AI answer share"
      description="How often stored AI answers name the brand, and its share of voice, by the question's topic. Week-by-week trend and citations of compare pages are included. This does not start a new run. Agents use get_ai_answer_share."
      action={<Button variant="ghost" onClick={() => go("ai-visibility")}>Open AI visibility</Button>}
    />
  );
  if (error) return <div className="space-y-6">{header}<ErrorText>{error}</ErrorText></div>;
  if (!data) return <div className="space-y-6">{header}<Loading /></div>;

  const t = data.totals;
  const top = t.competitors?.[0];

  return (
    <div className="space-y-6">
      {header}
      <p className="rounded-lg border bg-muted/30 px-3 py-2 text-[13px] text-muted-foreground">{data.note}</p>
      <div className="flex flex-wrap gap-2">
        {[28, 84, 365].map((d) => (
          <Button key={d} variant={days === d ? "primary" : "ghost"} onClick={() => setDays(d)}>{d} days</Button>
        ))}
      </div>
      <Stats cols={4}>
        <Stat label="Mention rate" value={pct(t.mention_rate)} sub={`${num(t.answers)} stored answers`} />
        <Stat label="Share of voice" value={pct(t.share_of_voice)} sub={top ? `${top.name} ${pct(top.share_of_voice)}` : "No competitors named"} />
        <Stat label="Topics" value={num(t.clusters)} sub={`${num(t.questions)} questions`} />
        <Stat label="Compare pages cited" value={pct(t.compare_citation_rate)} sub={`${num(data.compare.brand_cited)} answers cite a brand /compare page`} />
      </Stats>
      {t.answers === 0 ? (
        <Empty title="No stored AI answers in this window" action={<Button onClick={() => go("ai-visibility")}>Open AI visibility</Button>}>
          AI answer share is counted from question sets you already run. Start a run on AI visibility; this page does not sample answers or call a paid provider.
        </Empty>
      ) : (
        <>
          <GroupTable title="By topic" hint="A cluster is the topic on the question. Questions without a topic are Untagged and are not counted in the topic total above." rows={data.clusters} />
          <GroupTable title="By question set" rows={data.question_sets} />
          <GroupTable title="By question type" rows={data.question_types} />
          <div className="space-y-2">
            <h2 className="text-sm font-medium">Compare page citations</h2>
            <p className="text-xs text-muted-foreground">A cited URL counts when its path is /compare, including locale prefixes such as /zh/compare. Other paths that only contain the word compare are left out.</p>
            <Stats cols={3}>
              <Stat label="Brand /compare" value={pct(data.compare.brand_rate)} sub={`${num(data.compare.brand_cited)} of ${num(data.compare.answers)} answers`} />
              <Stat label="Competitor /compare" value={pct(data.compare.competitor_rate)} sub={`${num(data.compare.competitor_cited)} of ${num(data.compare.answers)} answers`} />
            </Stats>
            <DataTable
              data={data.compare.pages}
              rowId={(r) => `${r.owner}:${r.url}`}
              searchPlaceholder="Search cited pages"
              empty="No stored citations of compare pages in this window."
              columns={[
                { id: "url", header: "Page", accessorFn: (r) => r.url, cell: ({ row }) => <span className="font-mono text-[12px] break-all">{row.original.url}</span> },
                { id: "owner", header: "Owner", accessorFn: (r) => r.owner, cell: ({ row }) => <span>{row.original.owner}{row.original.competitor ? ` · ${row.original.competitor}` : ""}</span> },
                { id: "answers", header: "Answers", accessorFn: (r) => r.answers, meta: { align: "right", mono: true }, cell: ({ row }) => num(row.original.answers) },
              ]}
            />
          </div>
          <div className="space-y-2">
            <h2 className="text-sm font-medium">Week by week</h2>
            <DataTable
              data={data.weeks}
              rowId={(r) => r.week}
              empty="No weekly points yet."
              columns={[
                { id: "week", header: "Week", accessorFn: (r) => r.week, cell: ({ row }) => row.original.week },
                { id: "answers", header: "Answers", accessorFn: (r) => r.answers, meta: { align: "right", mono: true }, cell: ({ row }) => num(row.original.answers) },
                { id: "mention", header: "Mention rate", accessorFn: (r) => r.mention_rate ?? -1, meta: { align: "right", mono: true }, cell: ({ row }) => pct(row.original.mention_rate) },
                { id: "sov", header: "Share of voice", accessorFn: (r) => r.share_of_voice ?? -1, meta: { align: "right", mono: true }, cell: ({ row }) => pct(row.original.share_of_voice) },
                { id: "compare", header: "Compare cited", accessorFn: (r) => r.compare_citation_rate ?? -1, meta: { align: "right", mono: true }, cell: ({ row }) => pct(row.original.compare_citation_rate) },
              ]}
            />
          </div>
        </>
      )}
    </div>
  );
}

function GroupTable({ title, hint, rows }: { title: string; hint?: string; rows: Group[] }) {
  return (
    <div className="space-y-2">
      <h2 className="text-sm font-medium">{title}</h2>
      {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
      <DataTable
        data={rows}
        rowId={(r) => r.kind + ":" + r.key}
        searchPlaceholder="Search"
        empty="Nothing in this grouping yet."
        mobile={(r) => (
          <div className="space-y-1">
            <div className="text-[13px] font-medium">{r.label}</div>
            <div className="text-xs text-muted-foreground">Named {pct(r.mention_rate)} · share {pct(r.share_of_voice)} · {num(r.answers)} answers</div>
          </div>
        )}
        columns={[
          { id: "label", header: "Name", accessorFn: (r) => r.label, cell: ({ row }) => (
            <div>
              <div>{row.original.label}</div>
              <div className="text-xs text-muted-foreground">{num(row.original.questions)} questions · {rivals(row.original.competitors)}</div>
            </div>
          ) },
          { id: "answers", header: "Answers", accessorFn: (r) => r.answers, meta: { align: "right", mono: true }, cell: ({ row }) => num(row.original.answers) },
          { id: "mention", header: "Mention rate", accessorFn: (r) => r.mention_rate ?? -1, meta: { align: "right", mono: true }, cell: ({ row }) => pct(row.original.mention_rate) },
          { id: "sov", header: "Share of voice", accessorFn: (r) => r.share_of_voice ?? -1, meta: { align: "right", mono: true }, cell: ({ row }) => pct(row.original.share_of_voice) },
          { id: "recommend", header: "Recommended", accessorFn: (r) => r.recommend_rate ?? -1, meta: { align: "right", mono: true }, cell: ({ row }) => pct(row.original.recommend_rate) },
          { id: "compare", header: "Compare cited", accessorFn: (r) => r.compare_citation_rate ?? -1, meta: { align: "right", mono: true }, cell: ({ row }) => pct(row.original.compare_citation_rate) },
        ]}
      />
    </div>
  );
}

function rivals(list: Rival[] | undefined) {
  if (!list || list.length === 0) return "no competitors named";
  return list.slice(0, 3).map((r) => `${r.name} ${pct(r.share_of_voice)}`).join(" · ");
}
