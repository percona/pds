import { fileURLToPath, URL } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
      // The shared reader lives outside this package. Node would walk up from
      // that file and miss this package's typescript install.
      typescript: fileURLToPath(
        new URL("./node_modules/typescript/lib/typescript.js", import.meta.url),
      ),
    },
  },
});
