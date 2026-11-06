import { fileURLToPath, URL } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  // The host root serves the landing page; the console lives under /app/.
  base: "/app/",
  build: {
    // dist/ is the image's web root, so the console lands at /app/ in it.
    outDir: "dist/app",
  },
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  server: {
    port: 5173,
    // Quick tunnels send Host: *.trycloudflare.com. Vite 8 rejects unknown hosts.
    allowedHosts: [".trycloudflare.com"],
    proxy: {
      "/api": {
        target: process.env.ADAPTER_URL || "http://localhost:8080",
        changeOrigin: true,
        timeout: 180_000,
        proxyTimeout: 180_000,
      },
    },
  },
});
