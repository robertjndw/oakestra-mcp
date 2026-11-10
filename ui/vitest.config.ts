import { defineConfig, mergeConfig } from 'vitest/config';

import viteConfig from './vite.config.ts';

// Built on vite.config.ts so `virtual:oakestra-addon` resolves in tests too. Module Federation
// switches itself off under Vitest.
export default mergeConfig(
  viteConfig,
  defineConfig({
    test: {
      environment: 'jsdom',
      // The Tailwind entry is built by the Vite plugin; tests do not need styles.
      css: false,
      setupFiles: ['./test/cleanup.ts'],
    },
  }),
);
