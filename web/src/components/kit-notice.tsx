// KitNotice is the banner on a draft that a flow kit added: which kit it
// came from, and what to check before turning it on.

import { useEffect, useState } from "react";
import { Sparkles } from "lucide-react";
import { api } from "../api";

const titles = new Map<string, string>();

function useKitTitle(id: string) {
  const [title, setTitle] = useState(titles.get(id) ?? "");
  useEffect(() => {
    if (titles.has(id)) return;
    api.GET("/v1/kits/{id}", { params: { path: { id } } }).then(({ data }) => {
      if (!data) return;
      titles.set(id, data.title);
      setTitle(data.title);
    });
  }, [id]);
  return title;
}

export function KitNotice({ kit, then }: { kit: { id: string; key: string }; then: string }) {
  const title = useKitTitle(kit.id);
  return (
    <div className="flex items-start gap-2 rounded-md border border-violet-500/40 bg-violet-500/5 px-3 py-2 text-[13px]">
      <Sparkles className="mt-0.5 size-4 shrink-0 text-violet-600 dark:text-violet-400" />
      <span>
        From the setup kit <span className="font-medium">{title || kit.id}</span>, added as a draft so nothing runs until you say so. {then}
      </span>
    </div>
  );
}
