import { defineConfig } from "vitest/config";

// Unit tests cover the pure logic only (no React Native imports), so they run in plain Node.
export default defineConfig({
  test: {
    environment: "node",
    include: ["src/**/*.test.ts"],
  },
});
