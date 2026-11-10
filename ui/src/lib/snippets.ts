// Config snippets for the Connect page. They are plain string builders so the exact text that
// ends up in the clipboard is covered by tests.

export const SERVER_NAME = 'oakestra';
export const TOKEN_PLACEHOLDER = '<refresh_token>';
const API_PLACEHOLDER = '<oakestra api>';

/** The MCP URL that goes through the dashboard's addon proxy, for setups without a public URL. */
export function proxiedMcpUrl(origin: string, mcpPath = '/mcp'): string {
  const path = mcpPath.startsWith('/') ? mcpPath : `/${mcpPath}`;
  return `${origin}/addon-assets/oakestra-mcp/api${path}`;
}

export function loginCommand(apiUrl = API_PLACEHOLDER): string {
  return [
    `curl -X POST ${apiUrl}/api/auth/login \\`,
    `  -H 'content-type: application/json' \\`,
    `  -d '{"username":"...","password":"..."}'`,
  ].join('\n');
}

export function claudeCodeCommand(url: string, token = TOKEN_PLACEHOLDER): string {
  return `claude mcp add --transport http ${SERVER_NAME} ${url} --header "Authorization: Bearer ${token}"`;
}

export function cursorConfig(url: string, token = TOKEN_PLACEHOLDER): string {
  return JSON.stringify(
    { mcpServers: { [SERVER_NAME]: { url, headers: { Authorization: `Bearer ${token}` } } } },
    null,
    2,
  );
}

// VS Code names the top-level key "servers" and wants the transport spelled out.
export function vscodeConfig(url: string, token = TOKEN_PLACEHOLDER): string {
  return JSON.stringify(
    {
      servers: {
        [SERVER_NAME]: { type: 'http', url, headers: { Authorization: `Bearer ${token}` } },
      },
    },
    null,
    2,
  );
}

/** A raw JSON-RPC call, to check the endpoint and the token without an MCP client. */
export function genericCurl(url: string, token = TOKEN_PLACEHOLDER): string {
  return [
    `curl -X POST ${url} \\`,
    `  -H "Authorization: Bearer ${token}" \\`,
    `  -H 'content-type: application/json' \\`,
    `  -H 'accept: application/json, text/event-stream' \\`,
    `  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'`,
  ].join('\n');
}
