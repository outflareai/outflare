// A data table for list pages: sortable columns, a search box, column
// visibility, clickable rows, and a card list on phones. Built on TanStack
// Table and the shadcn Table.

import { useState, type ReactNode } from "react";
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
import { ArrowDown, ArrowUp, ChevronsUpDown, Columns3, Search } from "lucide-react";
import { cn } from "@/lib/utils";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { DropdownMenu, DropdownMenuCheckboxItem, DropdownMenuContent, DropdownMenuLabel, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Button } from "@/components/ui/button";

export type { ColumnDef };

type Props<T> = {
  columns: ColumnDef<T, unknown>[];
  data: T[];
  /** Stable key for each row. */
  rowId: (row: T) => string;
  onRowClick?: (row: T) => void;
  /** Filters rows in the browser. Leave out when the page searches on the server. */
  searchPlaceholder?: string;
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
  className?: string;
};

export function DataTable<T>({ columns, data, rowId, onRowClick, searchPlaceholder, toolbar, mobile, empty = "Nothing here yet.", initialSort = [], hidden = [], columnsMenu = true, className }: Props<T>) {
  const [sorting, setSorting] = useState<SortingState>(initialSort);
  const [filter, setFilter] = useState("");
  const [visibility, setVisibility] = useState<VisibilityState>(() => Object.fromEntries(hidden.map((h) => [h, false])));
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
  });
  const rows = table.getRowModel().rows;
  const hideable = !columnsMenu ? [] : table.getAllLeafColumns().filter((c) => c.getCanHide() && typeof c.columnDef.header === "string");
  const showToolbar = searchPlaceholder || toolbar || hideable.length > 3;

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
          {hideable.length > 3 && (
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
                  const meta = h.column.columnDef.meta as { align?: "right" } | undefined;
                  const sorted = h.column.getIsSorted();
                  return (
                    <TableHead key={h.id} className={cn("h-9 px-3 font-mono text-[11px] font-medium tracking-wide text-muted-foreground uppercase first:pl-4 last:pr-4", meta?.align === "right" && "text-right")} aria-sort={sorted === "asc" ? "ascending" : sorted === "desc" ? "descending" : undefined}>
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
                  const meta = c.column.columnDef.meta as { align?: "right"; mono?: boolean } | undefined;
                  return (
                    <TableCell key={c.id} className={cn("px-3 py-2.5 align-top whitespace-normal first:pl-4 last:pr-4", meta?.align === "right" && "text-right", meta?.mono && "font-mono text-[12px] tabular-nums")}>
                      {flexRender(c.column.columnDef.cell, c.getContext())}
                    </TableCell>
                  );
                })}
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </div>
  );
}
