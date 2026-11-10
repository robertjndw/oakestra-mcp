import path from 'node:path';

import { oakestraAddon } from '@oakestra/dashboard-sdk/vite';
import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

// The SDK and @oakestra/ui are linked from a dashboard checkout next to this repository.
const dashboardPackages = path.resolve(import.meta.dirname, '../../oakestra-dashboard-v2/packages');

export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
    // `name` is the addon name in /ui-addons and ADDON_ID; `prefix` must equal the one in
    // prefix(...) in src/addon.css.
    oakestraAddon({ name: 'oakestra-mcp', prefix: 'mcp', port: 5181 }),
  ],
  server: {
    // The linked sources live outside this folder.
    fs: { allow: [import.meta.dirname, dashboardPackages] },
    proxy: {
      // Same contract as the addon container's nginx: /api is the backend with the prefix stripped.
      '/api': {
        target: 'http://127.0.0.1:8090',
        changeOrigin: true,
        rewrite: (p) => p.replace(/^\/api/, ''),
      },
    },
  },
});
