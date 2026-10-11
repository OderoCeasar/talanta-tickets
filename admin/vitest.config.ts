import { defineConfig } from "vitest/config";

// Unit tests cover the pure logic only (money, time, geometry, colour), so they run in plain Node.
export default defineConfig({
  test: {
    environment: "node",
    include: ["src/**/*.test.ts"],
  },
});
