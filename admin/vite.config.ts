import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [react()],
  server: {
    // In production Caddy serves this build and proxies /v1 to the API on the same origin.
    // Mirror that in dev so the client never needs an API base URL.
    proxy: {
      "/v1": "http://localhost:8080",
    },
  },
});
