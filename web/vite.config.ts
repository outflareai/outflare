import { readFileSync } from "node:fs";
import { resolve } from "node:path";
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

// An operator build may supply private page implementations without adding them
// to the open app. The default build has no extensions. This is a local build
// input, never a URL or anything a workspace/request can choose.
export function pageExtensions(entry = process.env.OUTFLARE_WEB_EXTENSION_ENTRY): Plugin {
  const id = "virtual:outflare-pages";
  return {
    name: "outflare-page-extensions",
    resolveId(source) { return source === id ? "\0" + id : undefined; },
    load(source) {
      if (source !== "\0" + id) return;
      if (!entry) return "export const pages = {};";
      const file = resolve(entry);
      this.addWatchFile(file);
      return `export { pages } from ${JSON.stringify(file)};`;
    },
  };
}

function staleBuildReload(): Plugin {
  const file = decodeURIComponent(new URL("./stale-build.js", import.meta.url).pathname);
  return {
    name: "outflare-stale-build-reload",
    transformIndexHtml: () => [{ tag: "script", children: readFileSync(file, "utf8"), injectTo: "head" }],
  };
}

// In development the engine runs on :8080; proxy API calls to it.
export default defineConfig({
  plugins: [react(), tailwindcss(), changelog(), pageExtensions(), staleBuildReload()],
  resolve: { dedupe: ["react", "react-dom"], alias: { "@": decodeURIComponent(new URL("./src", import.meta.url).pathname) } },
  build: { rolldownOptions: { input: { app: decodeURIComponent(new URL("./index.html",import.meta.url).pathname), portal: decodeURIComponent(new URL("./portal.html",import.meta.url).pathname) } } },
  server: {
    proxy: {
      "/v1": "http://localhost:8080",
      "/healthz": "http://localhost:8080",
      "/readyz": "http://localhost:8080",
    },
  },
});
