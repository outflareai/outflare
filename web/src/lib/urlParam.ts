// A page's own state in the URL (?view=plans, ?step=wait-1), so a full
// page opened from a list has an address: Back closes it, a refresh keeps
// it and a link opens it. The app drops these when the page changes
// (pageParams, read in App's URL sync).

import { useCallback, useEffect, useState } from "react";

/** Every parameter a page keeps this way; cleared when the path changes. */
export const pageParams = new Set<string>();

const read = (name: string) => new URLSearchParams(location.search).get(name) ?? "";

/** The value of ?name= and a setter that adds a history entry ("" removes it). */
export function useUrlParam(name: string): [string, (value: string) => void] {
  pageParams.add(name);
  const [value, setValue] = useState(() => read(name));
  useEffect(() => {
    const sync = () => setValue(read(name));
    addEventListener("popstate", sync);
    return () => removeEventListener("popstate", sync);
  }, [name]);
  const set = useCallback(
    (next: string) => {
      if (read(name) === next) return setValue(next);
      const p = new URLSearchParams(location.search);
      if (next) p.set(name, next);
      else p.delete(name);
      const qs = p.toString();
      history.pushState(history.state, "", location.pathname + (qs ? `?${qs}` : "") + location.hash);
      setValue(next);
    },
    [name],
  );
  return [value, set];
}
