// A data table for list pages: sortable columns, a search box, column
// visibility, clickable rows, a card list on phones, and a CSV download of
// the rows and columns shown. Built on TanStack Table and the shadcn Table.

import { useEffect, useState, type ReactNode } from "react";
import { toast } from "@/ui";
import { track } from "@/lib/analytics";
import { dropTableView, savedView, saveTableView, syncTableViews } from "@/lib/table-views";
import {
  flexRender,
  getCoreRowModel,
  getFilteredRowModel,
  getSortedRowModel,
  useReactTable,
  type ColumnDef,
  type SortingState,
  type VisibilityState,
} from "@tanstack/react-table";
import { ArrowDown, ArrowUp, ChevronsUpDown, Columns3, Download, Search } from "lucide-react";
import { downloadCsv, toCsv, type CsvValue } from "@/lib/csv";
import { cn } from "@/lib/utils";
import { Table, TableBody, TableCell, TableFooter, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { DropdownMenu, DropdownMenuCheckboxItem, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Button } from "@/components/ui/button";

export type { ColumnDef };

/** A column's meta. csv is the column's value in a download (default its accessor's value); csvHeader names a column whose header isn't text; csv false leaves it out. */
export type ColumnMeta<T> = { align?: "right"; mono?: boolean; pin?: "right"; csv?: ((row: T) => CsvValue) | false; csvHeader?: string };

/** What a "Download CSV" button saves: the file's name and what to record when it is pressed (rows is how many). */
/** A total row under the table: its label sits in the first named column, and `cells` holds what shows under each column, by column id. */
export type TableFooterRow = { label: string; testId?: string; cells: Record<string, ReactNode> };

export type TableDownload = { file: string; label?: string; onDownload?: (rows: number) => void };

// Opaque versions of the row's colors, so cells scrolled under a pinned column don't show through.
const pinHead = "sticky right-0 z-[1] border-l bg-[color-mix(in_oklab,var(--card),var(--muted)_40%)]";
const pinCell = "sticky right-0 z-[1] border-l bg-card group-hover:bg-[color-mix(in_oklab,var(--card),var(--muted)_50%)]";

type Props<T> = {
  columns: ColumnDef<T, unknown>[];
  data: T[];
  /** Stable key for each row. */
  rowId: (row: T) => string;
  onRowClick?: (row: T) => void;
  /** Shows a search box. Without `search` it filters the rows in the browser. */
  searchPlaceholder?: string;
  /** The server searches and orders the rows: the box shows this query, and the browser neither filters nor sorts. */
  search?: { value: string; onChange: (q: string) => void };
  /** Extra controls next to the search box, such as filters. */
  toolbar?: ReactNode;
  /** One row as a compact card, shown instead of the table on phones. */
  mobile?: (row: T) => ReactNode;
  empty?: ReactNode;
  initialSort?: SortingState;
  /** Columns hidden until the viewer turns them on. */
  hidden?: string[];
  /** Offer the "Columns" menu when there are more than three columns to hide. */
  columnsMenu?: boolean;
  /**
   * Names this table's column view, so the viewer can save the columns they
   * picked as their default for every table with the same name (kept
   * with their account, so it follows them to every device). The Columns menu then always shows, with "Save as my
   * default" and "Back to standard columns".
   */
  view?: string;
  /** Shows a "Download CSV" button that saves exactly the rows and columns shown, in their order. */
  download?: TableDownload;
  /** Total rows under the table, worked out from the rows the search leaves. */
  footer?: (rows: T[]) => TableFooterRow[];
  /** Ticks on each row and a "select all" in the header, for bulk actions: the ids ticked and what changes them. "Select all" ticks the rows the search and filters leave. */
  selection?: { ids: string[]; onChange: (ids: string[]) => void; label?: (row: T) => string };
  className?: string;
};

// selectColumn is the column of ticks a table with bulk actions starts with.
function selectColumn<T>(sel: NonNullable<Props<T>["selection"]>, rowId: (row: T) => string): ColumnDef<T, unknown> {
  const picked = new Set(sel.ids);
  return {
    id: "_select",
    enableSorting: false,
    enableHiding: false,
    meta: { csv: false },
    header: ({ table }) => {
      const shown = table.getFilteredRowModel().rows.map((r) => rowId(r.original));
      const all = shown.length > 0 && shown.every((id) => picked.has(id));
      const some = !all && shown.some((id) => picked.has(id));
      return (
        <input type="checkbox" aria-label="Select all shown" className="size-3.5 align-middle" checked={all} ref={(el) => { if (el) el.indeterminate = some; }}
          onClick={(e) => e.stopPropagation()} onChange={() => sel.onChange(all ? sel.ids.filter((id) => !shown.includes(id)) : [...new Set([...sel.ids, ...shown])])} />
      );
    },
    cell: ({ row }) => {
      const id = rowId(row.original);
      return (
        <input type="checkbox" aria-label={sel.label ? `Select ${sel.label(row.original)}` : "Select"} className="size-3.5 align-middle" checked={picked.has(id)}
          onClick={(e) => e.stopPropagation()} onChange={(e) => sel.onChange(e.target.checked ? [...sel.ids, id] : sel.ids.filter((x) => x !== id))} />
      );
    },
  };
}

export function DataTable<T>({ columns: given, data, rowId, onRowClick, searchPlaceholder, search, toolbar, mobile, empty = "Nothing here yet.", initialSort = [], hidden = [], columnsMenu = true, view, download, footer, selection, className }: Props<T>) {
  const columns = selection ? [selectColumn(selection, rowId), ...given] : given;
  const [sorting, setSorting] = useState<SortingState>(initialSort);
  const [localFilter, setLocalFilter] = useState("");
  const filter = search ? search.value : localFilter;
  const setFilter = search ? search.onChange : setLocalFilter;
  const [visibility, setVisibility] = useState<VisibilityState>(() => Object.fromEntries((savedView(view) ?? hidden).map((h) => [h, false])));
  const [hasDefault, setHasDefault] = useState(() => savedView(view) !== null);
  // The view saved with the account may arrive after the table first
  // draws (a new browser): take it once it is here.
  useEffect(() => {
    if (!view) return;
    let live = true;
    void syncTableViews().then(() => {
      const saved = savedView(view);
      if (!live || saved === null) return;
      setVisibility(Object.fromEntries(saved.map((h) => [h, false])));
      setHasDefault(true);
    });
    return () => { live = false; };
  }, [view]);
  const table = useReactTable({
    data,
    columns,
    getRowId: rowId,
    state: { sorting, globalFilter: filter, columnVisibility: visibility },
    onSortingChange: setSorting,
    onGlobalFilterChange: setFilter,
    onColumnVisibilityChange: setVisibility,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getFilteredRowModel: getFilteredRowModel(),
    manualFiltering: !!search,
    manualSorting: !!search,
    enableSorting: !search,
  });
  const rows = table.getRowModel().rows;
  const hideable = !columnsMenu ? [] : table.getAllLeafColumns().filter((c) => c.getCanHide() && typeof c.columnDef.header === "string");
  const showColumns = hideable.length > (view ? 0 : 3);
  const showToolbar = searchPlaceholder || toolbar || showColumns || download;
  const saveView = async () => {
    if (!view) return;
    const off = hideable.filter((c) => !c.getIsVisible()).map((c) => c.id);
    if (await saveTableView(view, off)) {
      setHasDefault(true);
      toast("Saved. These columns are your default, on every device you sign in on.");
      track("ui.table_view_saved", { view, shown: hideable.length - off.length, hidden: off.length });
    } else {
      toast("Couldn't save your columns. Try again.", "error");
      track("ui.table_view_saved", { view, outcome: "error", error_code: "save_failed" });
    }
  };
  const resetView = () => {
    if (!view) return;
    void dropTableView(view);
    setHasDefault(false);
    setVisibility(Object.fromEntries(hidden.map((h) => [h, false])));
    toast("Back to the standard columns.");
    track("ui.table_view_reset", { view });
  };
  // The download is what the table shows: its visible columns with a text
  // header, and its rows as filtered and sorted.
  const save = () => {
    if (!download) return;
    const cols = table.getVisibleLeafColumns().filter((c) => {
      const meta = c.columnDef.meta as ColumnMeta<T> | undefined;
      return meta?.csv !== false && (meta?.csvHeader || (typeof c.columnDef.header === "string" && c.columnDef.header !== ""));
    });
    const headers = cols.map((c) => (c.columnDef.meta as ColumnMeta<T> | undefined)?.csvHeader || (c.columnDef.header as string));
    const out = rows.map((r) =>
      cols.map((c) => {
        const meta = c.columnDef.meta as ColumnMeta<T> | undefined;
        if (typeof meta?.csv === "function") return meta.csv(r.original);
        const v = r.getValue(c.id);
        return typeof v === "string" || typeof v === "number" || typeof v === "boolean" ? v : v == null ? "" : String(v);
      }),
    );
    downloadCsv(download.file, toCsv(headers, out));
    download.onDownload?.(out.length);
  };

  return (
    <div className={cn("space-y-3", className)}>
      {showToolbar && (
        <div className="flex flex-wrap items-center gap-2">
          {searchPlaceholder && (
            <label className="relative min-w-0 flex-1 basis-56">
              <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
              <input
                type="search"
                aria-label={searchPlaceholder}
                placeholder={searchPlaceholder}
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
                className="h-8 w-full rounded-md border border-input bg-transparent pr-2.5 pl-8 text-[13px] shadow-xs outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/40 dark:bg-input/30"
              />
            </label>
          )}
          {toolbar}
          {download && (
            <Button variant="outline" size="sm" className={cn("text-[13px]", !showColumns && "ml-auto")} onClick={save} disabled={rows.length === 0} title={`Save these ${rows.length} rows as a CSV file`}>
              <Download className="size-3.5" />
              {download.label ?? "Download CSV"}
            </Button>
          )}
          {showColumns && (
            <DropdownMenu modal={false}>
              <DropdownMenuTrigger asChild>
                <Button variant="outline" size="sm" className="ml-auto hidden text-[13px] sm:inline-flex">
                  <Columns3 className="size-3.5" />
                  Columns
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="min-w-40">
                <DropdownMenuLabel className="font-mono text-[11px] text-muted-foreground uppercase">Show columns</DropdownMenuLabel>
                {hideable.map((c) => (
                  <DropdownMenuCheckboxItem key={c.id} className="text-[13px]" checked={c.getIsVisible()} onCheckedChange={(v) => c.toggleVisibility(!!v)} onSelect={(e) => e.preventDefault()}>
                    {c.columnDef.header as string}
                  </DropdownMenuCheckboxItem>
                ))}
                {view && (
                  <>
                    <DropdownMenuSeparator />
                    <DropdownMenuItem className="text-[13px]" onSelect={saveView}>Save as my default</DropdownMenuItem>
                    <DropdownMenuItem className="text-[13px]" onSelect={resetView} disabled={!hasDefault && hideable.every((c) => c.getIsVisible() !== hidden.includes(c.id))}>Back to standard columns</DropdownMenuItem>
                  </>
                )}
              </DropdownMenuContent>
            </DropdownMenu>
          )}
        </div>
      )}

      {mobile && (
        <ul className="divide-y overflow-hidden rounded-lg border bg-card sm:hidden">
          {rows.length === 0 && <li className="px-4 py-8 text-center text-[13px] text-muted-foreground">{filter ? "Nothing matches." : empty}</li>}
          {rows.map((r) => (
            <li key={r.id}>
              {onRowClick ? (
                <button className="w-full px-4 py-3 text-left text-[13px] hover:bg-accent/50" onClick={() => onRowClick(r.original)}>{mobile(r.original)}</button>
              ) : (
                <div className="px-4 py-3 text-[13px]">{mobile(r.original)}</div>
              )}
            </li>
          ))}
        </ul>
      )}

      <div className={cn("overflow-hidden rounded-lg border bg-card shadow-xs", mobile && "hidden sm:block")}>
        <Table className="text-[13px]">
          <TableHeader className="bg-muted/40">
            {table.getHeaderGroups().map((g) => (
              <TableRow key={g.id} className="hover:bg-transparent">
                {g.headers.map((h) => {
                  const meta = h.column.columnDef.meta as { align?: "right"; pin?: "right" } | undefined;
                  const sorted = h.column.getIsSorted();
                  return (
                    <TableHead key={h.id} className={cn("h-9 px-3 font-mono text-[11px] font-medium tracking-wide text-muted-foreground uppercase first:pl-4 last:pr-4", meta?.align === "right" && "text-right", meta?.pin && pinHead)} aria-sort={sorted === "asc" ? "ascending" : sorted === "desc" ? "descending" : undefined}>
                      {h.isPlaceholder ? null : h.column.getCanSort() ? (
                        <button className={cn("-mx-1 inline-flex items-center gap-1 rounded px-1 uppercase hover:text-foreground", meta?.align === "right" && "flex-row-reverse")} onClick={h.column.getToggleSortingHandler()}>
                          {flexRender(h.column.columnDef.header, h.getContext())}
                          {sorted === "asc" ? <ArrowUp className="size-3" /> : sorted === "desc" ? <ArrowDown className="size-3" /> : <ChevronsUpDown className="size-3 opacity-40" />}
                        </button>
                      ) : (
                        flexRender(h.column.columnDef.header, h.getContext())
                      )}
                    </TableHead>
                  );
                })}
              </TableRow>
            ))}
          </TableHeader>
          <TableBody>
            {rows.length === 0 && (
              <TableRow className="hover:bg-transparent">
                <TableCell colSpan={table.getVisibleLeafColumns().length} className="h-24 text-center text-muted-foreground">{filter ? "Nothing matches." : empty}</TableCell>
              </TableRow>
            )}
            {rows.map((r) => (
              <TableRow
                key={r.id}
                className={cn("group align-top", onRowClick && "cursor-pointer")}
                onClick={onRowClick ? (e) => !(e.target as HTMLElement).closest("button, a, input, select, [role=menuitem]") && onRowClick(r.original) : undefined}
              >
                {r.getVisibleCells().map((c) => {
                  const meta = c.column.columnDef.meta as { align?: "right"; mono?: boolean; pin?: "right" } | undefined;
                  return (
                    <TableCell key={c.id} className={cn("px-3 py-2.5 align-top whitespace-normal first:pl-4 last:pr-4", meta?.align === "right" && "text-right", meta?.mono && "font-mono text-[12px] tabular-nums", meta?.pin && pinCell)}>
                      {flexRender(c.column.columnDef.cell, c.getContext())}
                    </TableCell>
                  );
                })}
              </TableRow>
            ))}
          </TableBody>
          {footer && rows.length > 0 && (
            <TableFooter className="bg-muted/40 font-normal">
              {footer(rows.map((r) => r.original)).map((f) => {
                const cols = table.getVisibleLeafColumns();
                const labelAt = cols.findIndex((c) => typeof c.columnDef.header === "string" && c.columnDef.header !== "");
                return (
                  <TableRow key={f.label} className="hover:bg-transparent" data-testid={f.testId}>
                    {cols.map((c, i) => {
                      const meta = c.columnDef.meta as { align?: "right"; pin?: "right" } | undefined;
                      return (
                        <TableCell key={c.id} className={cn("px-3 py-2.5 whitespace-nowrap first:pl-4 last:pr-4", meta?.align === "right" && "text-right", meta?.pin && pinCell)}>
                          {i === labelAt ? <span className="font-medium">{f.label}</span> : f.cells[c.id] ?? null}
                        </TableCell>
                      );
                    })}
                  </TableRow>
                );
              })}
            </TableFooter>
          )}
        </Table>
      </div>
    </div>
  );
}
