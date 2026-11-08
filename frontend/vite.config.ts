import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// In dev, the Go server (screener serve) answers /api on 8090.
export default defineConfig({
  plugins: [react()],
  server: { port: 5180, proxy: { "/api": "http://127.0.0.1:8090" } },
});
