import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// SPA is embedded into agentd via go:embed against web/dist.
// Keep base "/" since the daemon serves the loader page at root.
export default defineConfig({
  plugins: [react()],
  base: "/",
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: false,
    target: "es2020",
  },
  server: {
    port: 5173,
    strictPort: true,
  },
});
