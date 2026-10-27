import { Badge } from "../ui";
import { useBrand } from "@/lib/brand";

// BrandBadge names the brand a record belongs to. It shows nothing for
// shared records, for a workspace without brands, or when the page is
// already narrowed to that brand.
export function BrandBadge({ id }: { id?: string | null }) {
  const { brand, byId } = useBrand();
  const b = byId(id);
  if (!b || b.id === brand?.id) return null;
  return <Badge title={`Belongs to ${b.name}`}>{b.name}</Badge>;
}
