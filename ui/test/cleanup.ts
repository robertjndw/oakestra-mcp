import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

// Testing Library only unmounts on its own when the runner exposes a global afterEach, and this
// config doesn't turn on Vitest's globals.
afterEach(cleanup);
