import { createRequire, registerHooks } from 'node:module';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

// Hooks only work when React exists once. In production the shell shares its copy; in tests this
// addon's node_modules and the dashboard checkout's (behind the linked SDK and @oakestra/ui) each
// have one. Vite's dedupe only covers code Vite transforms; dependencies run in Node and resolve
// on their own, so every react and react-dom import is sent to this package's copy here.
const localRequire = createRequire(path.resolve(import.meta.dirname, '../package.json'));
const REACT = /^react(?:-dom)?(?:\/|$)/;
// require.resolve runs these hooks too; without the guard it would recurse forever.
let resolving = false;

registerHooks({
  resolve(specifier, context, nextResolve) {
    if (resolving || !REACT.test(specifier)) return nextResolve(specifier, context);
    resolving = true;
    try {
      return { url: pathToFileURL(localRequire.resolve(specifier)).href, shortCircuit: true };
    } finally {
      resolving = false;
    }
  },
});
