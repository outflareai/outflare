import { Empty } from "@/ui";

// PickBrand stands in for a data page while all brands are picked: brand
// data belongs to one brand (plans/brands/README.md), so a page never mixes
// two brands' people, lists or events.
export function PickBrand({ what, brands }: { what: string; brands: number }) {
  return (
    <Empty title="Pick a brand">
      {what} belong to one brand. Pick {brands > 1 ? `one of your ${brands} brands` : "your brand"} in the brand switcher to see them.
    </Empty>
  );
}
