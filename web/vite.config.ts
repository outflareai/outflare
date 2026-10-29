import { readFileSync } from "node:fs";
import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// The app's "What's new" reads the newest releases from CHANGELOG.md at
// build time, so every server shows the notes for the version it runs.
// A build without the file (a copy of web/ on its own) gets an empty list.
const changelogId = "virtual:outflare-changelog";
function changelog(): Plugin {
  const file = decodeURIComponent(new URL("../CHANGELOG.md", import.meta.url).pathname);
  return {
    name: "outflare-changelog",
    resolveId: (id) => (id === changelogId ? "\0" + changelogId : undefined),
    load(id) {
      if (id !== "\0" + changelogId) return;
      let text = "";
      try {
        text = readFileSync(file, "utf8");
        this.addWatchFile(file);
      } catch {
        // No changelog in this build.
      }
      return `export default ${JSON.stringify(parseChangelog(text))};`;
    },
  };
}

type Release = { version: string; date: string; changes: { kind: string; text: string }[] };

// parseChangelog keeps the newest 40 releases' bullets, first line only.
export function parseChangelog(md: string, max = 40): Release[] {
  const out: Release[] = [];
  let cur: Release | undefined;
  let kind = "";
  for (const line of md.split("\n")) {
    const rel = /^## \[(\d[^\]]*)\] - (\d{4}-\d{2}-\d{2})/.exec(line);
    if (rel) {
      if (out.length === max) break;
      cur = { version: rel[1], date: rel[2], changes: [] };
      out.push(cur);
      kind = "";
      continue;
    }
    if (line.startsWith("## ")) {
      cur = undefined;
      continue;
    }
    const sec = /^### (\w+)/.exec(line);
    if (sec) kind = sec[1].toLowerCase();
    else if (cur && line.startsWith("- ")) cur.changes.push({ kind, text: line.slice(2).trim() });
  }
  return out.filter((r) => r.changes.length > 0);
}

// In development the engine runs on :8080; proxy API calls to it.
export default defineConfig({
  plugins: [react(), tailwindcss(), changelog()],
  resolve: { alias: { "@": decodeURIComponent(new URL("./src", import.meta.url).pathname) } },
  server: {
    proxy: {
      "/v1": "http://localhost:8080",
      "/healthz": "http://localhost:8080",
      "/readyz": "http://localhost:8080",
    },
  },
});
