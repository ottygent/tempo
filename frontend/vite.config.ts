import { defineConfig } from "vite";
import solid from "vite-plugin-solid";

export default defineConfig({
  plugins: [solid()],
  server: { port: 5173, proxy: { "/api": "http://127.0.0.1:8080" } },
  build: { target: "es2022", sourcemap: true },
  test: { environment: "jsdom" }
});
