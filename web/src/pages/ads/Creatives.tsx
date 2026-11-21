// The Creatives tab of Paid ads: how each ad did (Results, which the Ads
// page renders itself) and the pages a brand's creatives are made and
// kept on. Each page is a tab id of its own, so it has a link, and
// "<page>/<id>" is one item's own page.
import type { Brand } from "@outflare/sdk";
import { BrandKit } from "./BrandKit";
import { CreativeLibrary } from "./CreativeLibrary";
import { CreativeSkills } from "./CreativeSkills";
import { CreativeStart } from "./CreativeStart";
import { TalkingVideos } from "./TalkingVideos";

export const creativePages: { id: string; label: string }[] = [
  { id: "creative-start", label: "Start here" },
  { id: "creatives", label: "Results" },
  { id: "creative-library", label: "Library" },
  { id: "talking-videos", label: "Talking videos" },
  { id: "creative-skills", label: "Skills and prompts" },
  { id: "brand-kit", label: "Brand kit" },
];

// creativeItem reads "<page>/<id>" (or a bare page) from the link.
export function creativeItem(item: string | undefined): { page: string; id: string } | null {
  const [page, ...rest] = (item ?? "").split("/");
  return creativePages.some((c) => c.id === page) ? { page, id: rest.join("/") } : null;
}

type Props = { ws: string; brand: Brand; canManage: boolean; page: string; id: string; open: (item: string) => void; onConnectors: () => void; brandChanged?: () => void; openProfile?: () => void };

export function CreativePage({ ws, brand, canManage, page, id, open, onConnectors, brandChanged, openProfile }: Props) {
  const on = (p: string) => ({ item: id, open: (v: string) => open(`${p}/${v}`), back: () => open(p) });
  if (page === "talking-videos") return <TalkingVideos brand={brand} canManage={canManage} {...on(page)} onConnectors={onConnectors} />;
  if (page === "creative-skills") return <CreativeSkills brand={brand} canManage={canManage} {...on(page)} openCreative={(c) => open(`creative-library/${c}`)} onConnectors={onConnectors} />;
  if (page === "creative-start") return <CreativeStart brand={brand} canManage={canManage} open={open} onConnectors={onConnectors} openProfile={openProfile} />;
  if (page === "brand-kit") return <BrandKit brand={brand} ws={ws} canManage={canManage} {...on(page)} brandChanged={brandChanged} />;
  return <CreativeLibrary brand={brand} canManage={canManage} {...on("creative-library")} />;
}
