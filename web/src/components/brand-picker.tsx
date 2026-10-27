import type { Brand } from "@outflare/sdk";
import { Icon } from "../icons";
import { field } from "../ui";
import { DropdownMenu, DropdownMenuCheckboxItem, DropdownMenuContent, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";

// brandsLabel names a brand list in a few words: "Every brand" when empty.
export function brandsLabel(ids: string[] | undefined, brands: Brand[]): string {
  if (!ids || ids.length === 0) return "Every brand";
  const names = ids.map((id) => brands.find((b) => b.id === id)?.name).filter(Boolean) as string[];
  if (names.length === 0) return "No brand left";
  return names.length <= 2 ? names.join(", ") : `${names[0]} and ${names.length - 1} more`;
}

// BrandsPicker chooses the brands someone works on: every brand (an empty
// list), or some of them.
export function BrandsPicker({ value, brands, onChange, label, disabled, heading = "Works on" }: { value: string[]; brands: Brand[]; onChange: (ids: string[]) => void; label: string; disabled?: boolean; heading?: string }) {
  const toggle = (id: string, on: boolean) => onChange(on ? [...value, id] : value.filter((x) => x !== id));
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild disabled={disabled}>
        <button type="button" aria-label={label} className={`${field} inline-flex min-w-36 items-center gap-2 text-left`}>
          <span className="min-w-0 flex-1 truncate">{brandsLabel(value, brands)}</span>
          <Icon name="updown" className="size-3.5 shrink-0 text-muted-foreground" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="min-w-56">
        <DropdownMenuLabel className="font-mono text-[11px] text-muted-foreground uppercase">{heading}</DropdownMenuLabel>
        <DropdownMenuCheckboxItem className="text-[13px]" checked={value.length === 0} onCheckedChange={() => onChange([])} onSelect={(e) => e.preventDefault()}>
          Every brand
        </DropdownMenuCheckboxItem>
        <DropdownMenuSeparator />
        {brands.map((b) => (
          <DropdownMenuCheckboxItem key={b.id} className="text-[13px]" checked={value.includes(b.id!)} onCheckedChange={(on) => toggle(b.id!, !!on)} onSelect={(e) => e.preventDefault()}>
            <span className="min-w-0 flex-1 truncate">{b.name}</span>
            {b.website && <span className="truncate font-mono text-[11px] text-muted-foreground">{b.website}</span>}
          </DropdownMenuCheckboxItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
