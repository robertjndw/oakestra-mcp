import { defineConfig, mergeConfig } from 'vitest/config';

import viteConfig from './vite.config.ts';

// Built on vite.config.ts so `virtual:oakestra-addon` resolves in tests too. Module Federation
// switches itself off under Vitest.
export default mergeConfig(
  viteConfig,
  defineConfig({
    resolve: {
      // One copy of each singleton for the modules Vite transforms, including the linked SDK and
      // ui sources, which would otherwise find the dashboard checkout's copies.
      dedupe: [
        'react',
        'react-dom',
        'react-router',
        '@tanstack/react-query',
        '@testing-library/react',
        '@testing-library/dom',
      ],
    },
    test: {
      environment: 'jsdom',
      // The Tailwind entry is built by the Vite plugin; tests do not need styles.
      css: false,
      // single-react first: it has to be in place before anything imports React.
      setupFiles: ['./test/single-react.ts', './test/cleanup.ts'],
    },
  }),
);
