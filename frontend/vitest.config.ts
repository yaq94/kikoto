import path from "node:path";
import { defineConfig } from "vitest/config";

export default defineConfig({
  // The transport imports the client version banner; tests need the same
  // compile-time constant Vite supplies in a build.
  define: {
    __APP_VERSION__: JSON.stringify("v0.0.0-test"),
  },
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  test: {
    include: ["src/**/*.test.ts", "src/**/*.test.tsx"],
    coverage: {
      provider: "v8",
      reporter: ["text", "json", "html"],
      reportsDirectory: "./coverage",
      include: ["src/**/*.ts", "src/**/*.tsx"],
      exclude: ["src/**/*.test.ts", "src/**/*.test.tsx", "src/**/*.d.ts", "src/test-support/**"],
    },
  },
});
