import { describe, expect, it } from 'vitest';

import {
  claudeCodeCommand,
  cursorConfig,
  genericCurl,
  loginCommand,
  proxiedMcpUrl,
  vscodeConfig,
} from './snippets';

const URL = 'http://root.example:8090/mcp';

describe('snippets', () => {
  it('builds the dashboard-proxied URL', () => {
    expect(proxiedMcpUrl('https://dash.example')).toBe(
      'https://dash.example/addon-assets/oakestra-mcp/api/mcp',
    );
    expect(proxiedMcpUrl('https://dash.example', 'mcp')).toBe(
      'https://dash.example/addon-assets/oakestra-mcp/api/mcp',
    );
  });

  it('builds the Claude Code command', () => {
    expect(claudeCodeCommand(URL, 'tok')).toBe(
      'claude mcp add --transport http oakestra http://root.example:8090/mcp --header "Authorization: Bearer tok"',
    );
  });

  it('builds the Cursor config', () => {
    expect(JSON.parse(cursorConfig(URL, 'tok'))).toEqual({
      mcpServers: { oakestra: { url: URL, headers: { Authorization: 'Bearer tok' } } },
    });
  });

  it('builds the VS Code config with an explicit transport', () => {
    expect(JSON.parse(vscodeConfig(URL, 'tok'))).toEqual({
      servers: { oakestra: { type: 'http', url: URL, headers: { Authorization: 'Bearer tok' } } },
    });
  });

  it('uses a placeholder token by default', () => {
    expect(claudeCodeCommand(URL)).toContain('Bearer <refresh_token>');
    expect(genericCurl(URL)).toContain('Bearer <refresh_token>');
  });

  it('builds the login command', () => {
    expect(loginCommand('http://api:10000')).toContain('curl -X POST http://api:10000/api/auth/login');
  });
});
