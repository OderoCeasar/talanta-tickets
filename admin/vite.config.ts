import react from "@vitejs/plugin-react";
import { defineConfig, loadEnv } from "vite";

export default defineConfig(({ mode }) => {
  // ADMIN_API_PROXY_TARGET lets a developer point the dev server at an API on another port
  // (for example a stub) when 8080 is busy. Unset, the target is the local API as before.
  const env = loadEnv(mode, ".", "ADMIN_");
  return {
    plugins: [react()],
    server: {
      // In production Caddy serves this build and proxies /v1 to the API on the same origin.
      // Mirror that in dev so the client never needs an API base URL.
      proxy: {
        "/v1": env.ADMIN_API_PROXY_TARGET || "http://localhost:8080",
      },
    },
  };
});
