import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// `npm run dev` serves the frontend with hot reload and forwards /api to `npm run server`.
// That's on port 8080; if 8080 is taken, set the same PORT for both commands.
const apiPort = process.env.PORT || 8080;

export default defineConfig({
  plugins: [react()],
  server: { proxy: { "/api": `http://127.0.0.1:${apiPort}` } },
});
