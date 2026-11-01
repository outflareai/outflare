import type { SearchInsightRow } from "@outflare/sdk";
import { DataTable, type ColumnDef } from "@/components/data-table";
import { Button } from "../../ui";
import { columns, labels, displayCell, type Report } from "./model";
import { StateBadge, Tags } from "./Primitives";
export function InsightTable({
  report,
  rows,
  selected,
  setSelected,
  onOpen,
  tagsFor,
  selectable = true,
}: {
  report: Report;
  rows: SearchInsightRow[];
  selected: Set<string>;
  setSelected: (s: Set<string>) => void;
  onOpen: (r: SearchInsightRow) => void;
  tagsFor: (key: string) => string[];
  selectable?: boolean;
}) {
  const cols: ColumnDef<SearchInsightRow, unknown>[] = [
    {
      id: "select",
      header: "Select",
      enableSorting: false,
      enableHiding: false,
      cell: ({ row }) => (
        <input
          type="checkbox"
          aria-label={`Select ${row.original.page || row.original.query || row.original.key}`}
          checked={selected.has(row.original.key)}
          onClick={(e) => e.stopPropagation()}
          onChange={(e) => {
            const next = new Set(selected);
            if (e.target.checked) next.add(row.original.key);
            else next.delete(row.original.key);
            setSelected(next);
          }}
        />
      ),
    },
    ...columns[report].map(
      (key, i): ColumnDef<SearchInsightRow, unknown> => ({
        id: key,
        header:
          report === "completeness"
            ? ((
                {
                  before: "Visible clicks",
                  after: "Omitted clicks",
                  value: "Rows collected",
                } as Record<string, string>
              )[key] ?? labels[key])
            : labels[key],
        accessorFn: (r) => r[key] as string | number | undefined,
        cell: ({ row }) =>
          i === 0 ? (
            <div className="max-w-sm space-y-1">
              <button
                className="block max-w-full truncate text-left font-medium hover:underline"
                onClick={(e) => {
                  e.stopPropagation();
                  onOpen(row.original);
                }}
                title={displayCell(row.original, key)}
              >
                {displayCell(row.original, key)}
              </button>
              <Tags tags={tagsFor(row.original.key)} />
            </div>
          ) : key === "status" ? (
            <StateBadge status={row.original.status ?? "unknown"} />
          ) : (
            <span
              className="block max-w-xs truncate tabular-nums"
              title={displayCell(row.original, key)}
            >
              {displayCell(row.original, key)}
            </span>
          ),
      }),
    ),
    {
      id: "inspect",
      header: "",
      enableSorting: false,
      cell: ({ row }) => (
        <Button
          variant="ghost"
          onClick={(e) => {
            e.stopPropagation();
            onOpen(row.original);
          }}
        >
          Inspect
        </Button>
      ),
    },
  ];
  return (
    <DataTable
      data={rows}
      columns={selectable ? cols : cols.filter((c) => c.id !== "select")}
      rowId={(r) => r.key}
      searchPlaceholder="Search pages, queries or sources"
      onRowClick={onOpen}
      empty="No rows match these filters."
      mobile={(r) => (
        <div className="space-y-2">
          <p className="break-all font-medium">
            {r.page || r.query || r.detail || r.key}
          </p>
          <div className="flex flex-wrap gap-3 text-xs text-muted-foreground">
            {columns[report].slice(1, 4).map((k) => (
              <span key={k}>
                {labels[k]}: {displayCell(r, k)}
              </span>
            ))}
          </div>
          <Tags tags={tagsFor(r.key)} />
          <span className="text-xs">Inspect evidence and actions →</span>
        </div>
      )}
    />
  );
}
