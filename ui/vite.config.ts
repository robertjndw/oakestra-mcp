import { oakestraAddon } from '@oakestra/dashboard-sdk/vite';
import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
    // `name` is the addon name in /ui-addons and ADDON_ID; `prefix` must equal the one in
    // prefix(...) in src/addon.css.
    oakestraAddon({ name: 'oakestra-mcp', prefix: 'mcp', port: 5181 }),
  ],
  server: {
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
