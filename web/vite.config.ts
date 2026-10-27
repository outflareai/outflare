import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// In development the engine runs on :8080; proxy API calls to it.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { "@": decodeURIComponent(new URL("./src", import.meta.url).pathname) } },
  server: {
    proxy: {
      "/v1": "http://localhost:8080",
      "/healthz": "http://localhost:8080",
      "/readyz": "http://localhost:8080",
    },
  },
});
